// Package spannerexec adapts the official Cloud Spanner client to store.Executor.
package spannerexec

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/gcpauth"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type dataClient interface {
	Apply(context.Context, store.Mutation) error
	Query(context.Context, store.Statement) ([]store.Row, error)
	ReadWrite(context.Context, func(context.Context, transaction) error) error
	Close()
}

type transaction interface {
	Execute(context.Context, store.Statement) (int64, error)
	Apply(store.Mutation) error
}

type adminClient interface {
	UpdateDDL(context.Context, string, string) error
	Close() error
}

type clientsFactory func(context.Context, string, oauth2.TokenSource) (dataClient, adminClient, error)

var createClients clientsFactory = newLiveClients

// Executor is safe for concurrent use except Close, matching the underlying clients.
type Executor struct {
	data  dataClient
	admin adminClient
	once  sync.Once
	err   error
}

var _ store.Executor = (*Executor)(nil)

// New obtains a token through the Google credential selector before it
// creates either Google client. Selector.TokenSource enforces the GODEBUG PQ
// downgrade refusal and the exact identity/credential profile.
func New(ctx context.Context, databaseName string, selector gcpauth.Selector) (*Executor, error) {
	return newWithTokenSource(ctx, databaseName, func(ctx context.Context) (oauth2.TokenSource, error) {
		return selector.TokenSource(ctx)
	}, createClients)
}

func newWithTokenSource(ctx context.Context, databaseName string, tokens func(context.Context) (oauth2.TokenSource, error), factory clientsFactory) (*Executor, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, fmt.Errorf("Spanner executor requires an active context")
	}
	if databaseName == "" || factory == nil || tokens == nil {
		return nil, fmt.Errorf("Spanner executor requires database, token factory, and client factory")
	}
	token, err := tokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("select Spanner credentials before client construction: %w", err)
	}
	if token == nil {
		return nil, errors.New("select Spanner credentials returned no token source")
	}
	data, admin, err := factory(ctx, databaseName, token)
	if err != nil {
		return nil, fmt.Errorf("construct Spanner clients: %w", err)
	}
	if data == nil || admin == nil {
		if data != nil {
			data.Close()
		}
		if admin != nil {
			_ = admin.Close()
		}
		return nil, errors.New("construct Spanner clients returned an incomplete client set")
	}
	return &Executor{data: data, admin: admin}, nil
}

func (e *Executor) Query(ctx context.Context, statement store.Statement) ([]store.Row, error) {
	if err := active(ctx); err != nil {
		return nil, err
	}
	return e.data.Query(ctx, statement)
}

func (e *Executor) Execute(ctx context.Context, statement store.Statement) (int64, error) {
	if err := active(ctx); err != nil {
		return 0, err
	}
	var count int64
	err := e.data.ReadWrite(ctx, func(ctx context.Context, transaction transaction) error {
		var err error
		count, err = transaction.Execute(ctx, statement)
		return err
	})
	return count, err
}

func (e *Executor) Apply(ctx context.Context, mutation store.Mutation) error {
	if err := active(ctx); err != nil {
		return err
	}
	return e.data.Apply(ctx, mutation)
}

func (e *Executor) ReadWrite(ctx context.Context, operation func(store.Transaction) error) error {
	if err := active(ctx); err != nil {
		return err
	}
	if operation == nil {
		return errors.New("Spanner read-write operation is required")
	}
	return e.data.ReadWrite(ctx, func(ctx context.Context, tx transaction) error {
		return operation(transactionAdapter{ctx: ctx, tx: tx})
	})
}

func (e *Executor) UpdateDDL(ctx context.Context, statements []store.DDLStatement) error {
	if err := active(ctx); err != nil {
		return err
	}
	for _, statement := range statements {
		if statement.SQL == "" {
			return errors.New("Spanner DDL statement is required")
		}
		if err := e.admin.UpdateDDL(ctx, "", statement.SQL); err != nil && !(statement.AlreadyExistsOK && status.Code(err) == codes.AlreadyExists) {
			return fmt.Errorf("apply Spanner DDL: %w", err)
		}
	}
	return nil
}

func (e *Executor) Close() error {
	e.once.Do(func() {
		dataDone := make(chan struct{})
		go func() { e.data.Close(); close(dataDone) }()
		adminErr := e.admin.Close()
		<-dataDone
		e.err = adminErr
	})
	return e.err
}

func active(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Spanner executor context is required")
	}
	return ctx.Err()
}

type transactionAdapter struct {
	ctx context.Context
	tx  transaction
}

func (a transactionAdapter) Execute(_ context.Context, statement store.Statement) (int64, error) {
	return a.tx.Execute(a.ctx, statement)
}
func (a transactionAdapter) Apply(_ context.Context, mutation store.Mutation) error {
	return a.tx.Apply(mutation)
}

type liveData struct {
	apply     func(context.Context, store.Mutation) error
	query     func(context.Context, store.Statement) ([]store.Row, error)
	readWrite func(context.Context, func(context.Context, transaction) error) error
	close     func()
}

func (d liveData) Apply(ctx context.Context, mutation store.Mutation) error {
	return d.apply(ctx, mutation)
}
func (d liveData) Query(ctx context.Context, statement store.Statement) ([]store.Row, error) {
	return d.query(ctx, statement)
}
func (d liveData) ReadWrite(ctx context.Context, operation func(context.Context, transaction) error) error {
	return d.readWrite(ctx, operation)
}
func (d liveData) Close() { d.close() }

type liveTransaction struct {
	execute func(context.Context, store.Statement) (int64, error)
	apply   func(store.Mutation) error
}

func (t liveTransaction) Execute(ctx context.Context, statement store.Statement) (int64, error) {
	return t.execute(ctx, statement)
}
func (t liveTransaction) Apply(mutation store.Mutation) error {
	return t.apply(mutation)
}

func nativeMutations(mutation store.Mutation) []*spanner.Mutation {
	mutations := make([]*spanner.Mutation, 0, len(mutation.Values))
	for _, values := range mutation.Values {
		mutations = append(mutations, spanner.InsertOrUpdate(mutation.Table, mutation.Columns, values))
	}
	return mutations
}
func nativeStatement(statement store.Statement) spanner.Statement {
	return spanner.Statement{SQL: statement.SQL, Params: statement.Params}
}

type liveAdmin struct {
	update func(context.Context, string) error
	close  func() error
}

func (a liveAdmin) UpdateDDL(ctx context.Context, _ string, sql string) error {
	return a.update(ctx, sql)
}
func (a liveAdmin) Close() error { return a.close() }
func newLiveClients(ctx context.Context, databaseName string, token oauth2.TokenSource) (dataClient, adminClient, error) {
	opts := []option.ClientOption{option.WithTokenSource(token)}
	data, err := newDataClient(ctx, databaseName, opts...)
	if err != nil {
		return nil, nil, err
	}
	admin, err := newAdminClient(ctx, opts...)
	if err != nil {
		data.Close()
		return nil, nil, err
	}
	return bindData(data), bindAdmin(admin, databaseName), nil
}

var newDataClient = spanner.NewClient
var newAdminClient = database.NewDatabaseAdminClient

func bindData(client *spanner.Client) liveData {
	return liveData{apply: func(ctx context.Context, mutation store.Mutation) error {
		_, err := client.Apply(ctx, nativeMutations(mutation))
		return err
	}, query: func(ctx context.Context, statement store.Statement) ([]store.Row, error) {
		return collect(nativeResultIterator{iterator: client.Single().Query(ctx, nativeStatement(statement))})
	}, readWrite: func(ctx context.Context, operation func(context.Context, transaction) error) error {
		_, err := client.ReadWriteTransaction(ctx, func(ctx context.Context, tx *spanner.ReadWriteTransaction) error {
			return operation(ctx, bindTransaction(tx))
		})
		return err
	}, close: client.Close}
}
func bindTransaction(tx *spanner.ReadWriteTransaction) liveTransaction {
	return liveTransaction{execute: func(ctx context.Context, statement store.Statement) (int64, error) {
		return tx.Update(ctx, nativeStatement(statement))
	}, apply: func(mutation store.Mutation) error { return tx.BufferWrite(nativeMutations(mutation)) }}
}
func bindAdmin(client *database.DatabaseAdminClient, databaseName string) liveAdmin {
	return liveAdmin{update: func(ctx context.Context, sql string) error {
		operation, err := client.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{Database: databaseName, Statements: []string{sql}})
		if err != nil {
			return err
		}
		return operation.Wait(ctx)
	}, close: client.Close}
}

// resultRow is the read-only subset of a Cloud Spanner row used by the store.
// Keeping the adapter boundary this narrow lets the result decoder be tested
// without a network-backed Spanner client.
type resultRow interface {
	Size() int
	Column(int, interface{}) error
}

// resultIterator is the lifecycle subset of a Cloud Spanner row iterator.
type resultIterator interface {
	Next() (resultRow, error)
	Stop()
}

type nativeResultIterator struct{ iterator *spanner.RowIterator }

func (i nativeResultIterator) Next() (resultRow, error) { return i.iterator.Next() }
func (i nativeResultIterator) Stop()                    { i.iterator.Stop() }

func collect(rowsIterator resultIterator) ([]store.Row, error) {
	if rowsIterator == nil {
		return nil, errors.New("Spanner row iterator is required")
	}
	defer rowsIterator.Stop()
	return collectRows(func() (store.Row, error) {
		row, err := rowsIterator.Next()
		if err != nil {
			return nil, err
		}
		values := make([]spanner.GenericColumnValue, row.Size())
		for index := range values {
			if err := row.Column(index, &values[index]); err != nil {
				return nil, err
			}
		}
		return decodeRow(values)
	})
}

func collectRows(next func() (store.Row, error)) ([]store.Row, error) {
	var rows []store.Row
	for {
		row, err := next()
		if err == iterator.Done {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}
