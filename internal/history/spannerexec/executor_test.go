package spannerexec

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/spanner"
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/gcpauth"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeData struct {
	rows       []store.Row
	queryErr   error
	applyErr   error
	tx         *fakeTransaction
	closed     bool
	mutations  []store.Mutation
	statements []store.Statement
}

func (f *fakeData) Apply(_ context.Context, mutation store.Mutation) error {
	f.mutations = append(f.mutations, mutation)
	return f.applyErr
}
func (f *fakeData) Query(_ context.Context, statement store.Statement) ([]store.Row, error) {
	f.statements = append(f.statements, statement)
	return f.rows, f.queryErr
}
func (f *fakeData) ReadWrite(ctx context.Context, operation func(context.Context, transaction) error) error {
	return operation(ctx, f.tx)
}
func (f *fakeData) Close() { f.closed = true }

type fakeTransaction struct {
	count      int64
	executeErr error
	applyErr   error
	statements []store.Statement
	mutations  []store.Mutation
}

func (f *fakeTransaction) Execute(_ context.Context, statement store.Statement) (int64, error) {
	f.statements = append(f.statements, statement)
	return f.count, f.executeErr
}
func (f *fakeTransaction) Apply(mutation store.Mutation) error {
	f.mutations = append(f.mutations, mutation)
	return f.applyErr
}

type fakeAdmin struct {
	statements []string
	errs       map[string]error
	closed     bool
}

func (f *fakeAdmin) UpdateDDL(_ context.Context, _ string, statement string) error {
	f.statements = append(f.statements, statement)
	return f.errs[statement]
}
func (f *fakeAdmin) Close() error { f.closed = true; return nil }

type fakeResultRow struct {
	values    []spanner.GenericColumnValue
	columnErr map[int]error
}

func (r fakeResultRow) Size() int { return len(r.values) }
func (r fakeResultRow) Column(index int, destination interface{}) error {
	if err := r.columnErr[index]; err != nil {
		return err
	}
	target, ok := destination.(*spanner.GenericColumnValue)
	if !ok {
		return errors.New("unexpected column destination")
	}
	*target = r.values[index]
	return nil
}

type fakeResultIterator struct {
	steps []struct {
		row resultRow
		err error
	}
	stopped bool
}

func (i *fakeResultIterator) Next() (resultRow, error) {
	if len(i.steps) == 0 {
		return nil, iterator.Done
	}
	step := i.steps[0]
	i.steps = i.steps[1:]
	return step.row, step.err
}
func (i *fakeResultIterator) Stop() { i.stopped = true }

func testExecutor(t *testing.T) (*Executor, *fakeData, *fakeAdmin) {
	t.Helper()
	data := &fakeData{tx: &fakeTransaction{count: 7}}
	admin := &fakeAdmin{errs: map[string]error{}}
	executor, err := newWithTokenSource(context.Background(), "projects/p/instances/i/databases/d", func(context.Context) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"}), nil
	}, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		return data, admin, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor, data, admin
}

func TestExecutorDelegatesEveryStoreOperation(t *testing.T) {
	executor, data, admin := testExecutor(t)
	data.rows = []store.Row{{"chunk", int64(4)}}
	statement := store.Statement{SQL: "SELECT @id", Params: map[string]any{"id": "chunk"}}
	rows, err := executor.Query(context.Background(), statement)
	if err != nil || len(rows) != 1 || rows[0][0] != "chunk" {
		t.Fatalf("Query() = %#v, %v", rows, err)
	}
	if count, err := executor.Execute(context.Background(), statement); err != nil || count != 7 {
		t.Fatalf("Execute() = %d, %v", count, err)
	}
	mutation := store.Mutation{Table: "ConversationChunks", Columns: []string{"Id"}, Values: [][]any{{"chunk"}}}
	if err := executor.Apply(context.Background(), mutation); err != nil || len(data.mutations) != 1 {
		t.Fatalf("Apply() = %v, %#v", err, data.mutations)
	}
	if err := executor.ReadWrite(context.Background(), func(tx store.Transaction) error {
		if _, err := tx.Execute(context.Background(), statement); err != nil {
			return err
		}
		return tx.Apply(context.Background(), mutation)
	}); err != nil || len(data.tx.mutations) != 1 {
		t.Fatalf("ReadWrite() = %v, %#v", err, data.tx.mutations)
	}
	if err := executor.UpdateDDL(context.Background(), []store.DDLStatement{{SQL: "CREATE TABLE T"}}); err != nil || len(admin.statements) != 1 {
		t.Fatalf("UpdateDDL() = %v, %#v", err, admin.statements)
	}
	if err := executor.Close(); err != nil || !data.closed || !admin.closed {
		t.Fatalf("Close() = %v data=%t admin=%t", err, data.closed, admin.closed)
	}
}

func TestExecutorFailsClosedAtBoundaries(t *testing.T) {
	executor, data, admin := testExecutor(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Query(canceled, store.Statement{}); err == nil {
		t.Fatal("Query accepted cancelled context")
	}
	if err := executor.ReadWrite(context.Background(), nil); err == nil {
		t.Fatal("ReadWrite accepted nil operation")
	}
	if err := executor.UpdateDDL(context.Background(), []store.DDLStatement{{SQL: ""}}); err == nil {
		t.Fatal("UpdateDDL accepted empty SQL")
	}
	admin.errs["CREATE INDEX T"] = status.Error(codes.AlreadyExists, "exists")
	if err := executor.UpdateDDL(context.Background(), []store.DDLStatement{{SQL: "CREATE INDEX T", AlreadyExistsOK: true}}); err != nil {
		t.Fatalf("UpdateDDL already exists = %v", err)
	}
	if err := executor.UpdateDDL(context.Background(), []store.DDLStatement{{SQL: "CREATE INDEX T"}}); err == nil {
		t.Fatal("UpdateDDL accepted unexpected existing DDL")
	}
	data.tx.executeErr = errors.New("transaction failure")
	if count, err := executor.Execute(context.Background(), store.Statement{}); err == nil || count != 7 {
		t.Fatalf("Execute() = %d, %v", count, err)
	}
}

func TestExecutorRejectsInactiveContextsAndPropagatesStoreFailures(t *testing.T) {
	executor, data, _ := testExecutor(t)
	if err := active(nil); err == nil {
		t.Fatal("active accepted nil context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := active(canceled); err == nil {
		t.Fatal("active accepted cancelled context")
	}
	if err := executor.Apply(nil, store.Mutation{}); err == nil {
		t.Fatal("Apply accepted nil context")
	}
	if err := executor.Apply(canceled, store.Mutation{}); err == nil {
		t.Fatal("Apply accepted cancelled context")
	}
	data.applyErr = errors.New("apply failed")
	if err := executor.Apply(context.Background(), store.Mutation{}); err == nil {
		t.Fatal("Apply accepted data failure")
	}
	data.queryErr = errors.New("query failed")
	if _, err := executor.Query(context.Background(), store.Statement{}); err == nil {
		t.Fatal("Query accepted data failure")
	}
	if _, err := executor.Execute(nil, store.Statement{}); err == nil {
		t.Fatal("Execute accepted nil context")
	}
	if err := executor.ReadWrite(canceled, func(store.Transaction) error { return nil }); err == nil {
		t.Fatal("ReadWrite accepted cancelled context")
	}
	if err := executor.UpdateDDL(nil, []store.DDLStatement{{SQL: "DDL"}}); err == nil {
		t.Fatal("UpdateDDL accepted nil context")
	}
}

func TestConstructorOrdersCredentialSelectionBeforeClientConstruction(t *testing.T) {
	called := false
	_, err := newWithTokenSource(context.Background(), "db", func(context.Context) (oauth2.TokenSource, error) { return nil, errors.New("no credentials") }, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		called = true
		return nil, nil, nil
	})
	if err == nil || called {
		t.Fatalf("New attempted clients before token selection: err=%v called=%t", err, called)
	}
	data := &fakeData{tx: &fakeTransaction{}}
	admin := &fakeAdmin{}
	_, err = newWithTokenSource(context.Background(), "db", func(context.Context) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{}), nil
	}, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		return data, nil, nil
	})
	if err == nil || !data.closed || admin.closed {
		t.Fatalf("New did not close partial client: err=%v data=%t admin=%t", err, data.closed, admin.closed)
	}
	if _, err := newWithTokenSource(nil, "db", func(context.Context) (oauth2.TokenSource, error) { return nil, nil }, nil); err == nil {
		t.Fatal("New accepted nil context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newWithTokenSource(canceled, "db", func(context.Context) (oauth2.TokenSource, error) { return nil, nil }, nil); err == nil {
		t.Fatal("New accepted cancelled context")
	}
	for name, tokens := range map[string]func(context.Context) (oauth2.TokenSource, error){
		"nil token": func(context.Context) (oauth2.TokenSource, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newWithTokenSource(context.Background(), "db", tokens, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
				return nil, nil, nil
			}); err == nil {
				t.Fatal("New accepted invalid token result")
			}
		})
	}
	if _, err := newWithTokenSource(context.Background(), "db", func(context.Context) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{}), nil
	}, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		return nil, nil, errors.New("factory failed")
	}); err == nil {
		t.Fatal("New accepted factory failure")
	}
	if _, err := newWithTokenSource(context.Background(), "", func(context.Context) (oauth2.TokenSource, error) { return nil, nil }, func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		return nil, nil, nil
	}); err == nil {
		t.Fatal("New accepted empty database")
	}
}

func TestSelectorRefusesPQCDowngradeBeforeFactory(t *testing.T) {
	t.Setenv("GODEBUG", "tlsmlkem=0")
	called := false
	old := createClients
	createClients = func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error) {
		called = true
		return nil, nil, nil
	}
	t.Cleanup(func() { createClients = old })
	_, err := New(context.Background(), "db", gcpauth.Selector{CredentialsSource: gcpauth.CredentialsSourceApplicationDefault, CredentialsProfile: gcpauth.CredentialsProfileAttachedServiceAccount, CredentialsIdentity: "valid-name@valid-project.iam.gserviceaccount.com"})
	if err == nil || !strings.Contains(err.Error(), "tlsmlkem=0") || called {
		t.Fatalf("New PQ guard = %v, factory=%t", err, called)
	}
	if os.Getenv("GODEBUG") != "tlsmlkem=0" {
		t.Fatal("test lost GODEBUG refusal witness")
	}
}

func TestNativeAdaptersPreserveRowsMutationsAndConstructorCleanup(t *testing.T) {
	mutation := store.Mutation{Table: "T", Columns: []string{"Id"}, Values: [][]any{{"one"}, {"two"}}}
	if got := nativeMutations(mutation); len(got) != 2 {
		t.Fatalf("native mutations = %d", len(got))
	}
	statement := nativeStatement(store.Statement{SQL: "SELECT @id", Params: map[string]any{"id": "one"}})
	if statement.SQL != "SELECT @id" || statement.Params["id"] != "one" {
		t.Fatalf("native statement = %#v", statement)
	}
	sequence := []struct {
		row store.Row
		err error
	}{{row: store.Row{"one"}}, {row: store.Row{"two"}}, {err: iterator.Done}}
	rows, err := collectRows(func() (store.Row, error) { next := sequence[0]; sequence = sequence[1:]; return next.row, next.err })
	if err != nil || len(rows) != 2 || rows[1][0] != "two" {
		t.Fatalf("collectRows() = %#v, %v", rows, err)
	}
	if _, err := collectRows(func() (store.Row, error) { return nil, errors.New("query failed") }); err == nil {
		t.Fatal("collectRows accepted query error")
	}

	oldData := newDataClient
	t.Cleanup(func() { newDataClient = oldData })
	newDataClient = func(context.Context, string, ...option.ClientOption) (*spanner.Client, error) {
		return nil, errors.New("data failed")
	}
	if _, _, err := newLiveClients(context.Background(), "db", oauth2.StaticTokenSource(&oauth2.Token{})); err == nil {
		t.Fatal("newLiveClients accepted data failure")
	}
}

func TestCollectDecodesRowsInOrderAndStopsIterator(t *testing.T) {
	rowsIterator := &fakeResultIterator{steps: []struct {
		row resultRow
		err error
	}{
		{row: fakeResultRow{values: []spanner.GenericColumnValue{
			genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("first")),
			genericColumn(sppb.TypeCode_INT64, structpb.NewStringValue("1")),
		}}},
		{row: fakeResultRow{values: []spanner.GenericColumnValue{
			genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("second")),
			genericColumn(sppb.TypeCode_BOOL, structpb.NewBoolValue(true)),
		}}},
		{err: iterator.Done},
	}}
	rows, err := collect(rowsIterator)
	if err != nil {
		t.Fatalf("collect() error = %v", err)
	}
	want := []store.Row{{"first", int64(1)}, {"second", true}}
	if len(rows) != len(want) || rows[0][0] != want[0][0] || rows[0][1] != want[0][1] || rows[1][0] != want[1][0] || rows[1][1] != want[1][1] {
		t.Fatalf("collect() = %#v, want %#v", rows, want)
	}
	if !rowsIterator.stopped {
		t.Fatal("collect() did not stop its iterator")
	}
}

func TestCollectFailsClosedAndAlwaysStopsIterator(t *testing.T) {
	columnFailure := errors.New("column failed")
	nextFailure := errors.New("next failed")
	unsupported := genericColumn(sppb.TypeCode_DATE, structpb.NewStringValue("2026-09-09"))
	for name, rowsIterator := range map[string]*fakeResultIterator{
		"next error": {steps: []struct {
			row resultRow
			err error
		}{{err: nextFailure}}},
		"column error": {steps: []struct {
			row resultRow
			err error
		}{{row: fakeResultRow{values: []spanner.GenericColumnValue{genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("x"))}, columnErr: map[int]error{0: columnFailure}}}}},
		"decode error": {steps: []struct {
			row resultRow
			err error
		}{{row: fakeResultRow{values: []spanner.GenericColumnValue{unsupported}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if rows, err := collect(rowsIterator); err == nil || rows != nil {
				t.Fatalf("collect() = %#v, %v; want no rows and error", rows, err)
			}
			if !rowsIterator.stopped {
				t.Fatal("collect() did not stop iterator after failure")
			}
		})
	}
	if rows, err := collect(nil); err == nil || rows != nil {
		t.Fatalf("collect(nil) = %#v, %v; want no rows and error", rows, err)
	}
}

func TestLiveAdapterSeamsDelegateWithoutNetwork(t *testing.T) {
	if bound := bindData(nil); bound.apply == nil || bound.query == nil || bound.readWrite == nil || bound.close == nil {
		t.Fatal("bindData did not create every adapter closure")
	}
	if bound := bindTransaction(nil); bound.execute == nil || bound.apply == nil {
		t.Fatal("bindTransaction did not create every adapter closure")
	}
	if bound := bindAdmin(nil, "db"); bound.update == nil || bound.close == nil {
		t.Fatal("bindAdmin did not create every adapter closure")
	}
	closed := false
	live := liveData{apply: func(context.Context, store.Mutation) error { return nil }, query: func(context.Context, store.Statement) ([]store.Row, error) { return []store.Row{{"row"}}, nil }, readWrite: func(ctx context.Context, operation func(context.Context, transaction) error) error {
		return operation(ctx, liveTransaction{execute: func(context.Context, store.Statement) (int64, error) { return 3, nil }, apply: func(store.Mutation) error { return nil }})
	}, close: func() { closed = true }}
	if err := live.Apply(context.Background(), store.Mutation{}); err != nil {
		t.Fatal(err)
	}
	if rows, err := live.Query(context.Background(), store.Statement{}); err != nil || rows[0][0] != "row" {
		t.Fatalf("live query = %#v %v", rows, err)
	}
	if err := live.ReadWrite(context.Background(), func(_ context.Context, tx transaction) error {
		if count, err := tx.Execute(context.Background(), store.Statement{}); err != nil || count != 3 {
			return errors.New("transaction seam")
		}
		return tx.Apply(store.Mutation{})
	}); err != nil {
		t.Fatal(err)
	}
	live.Close()
	if !closed {
		t.Fatal("live close not delegated")
	}
	adminClosed := false
	admin := liveAdmin{update: func(_ context.Context, sql string) error {
		if sql != "DDL" {
			return errors.New("wrong ddl")
		}
		return nil
	}, close: func() error { adminClosed = true; return nil }}
	if err := admin.UpdateDDL(context.Background(), "ignored", "DDL"); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil || !adminClosed {
		t.Fatalf("admin close = %v %t", err, adminClosed)
	}
}
