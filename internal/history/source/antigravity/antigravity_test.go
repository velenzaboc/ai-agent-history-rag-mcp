package antigravity

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChunksGolden(t *testing.T) {
	p, e := os.ReadFile(filepath.Join("testdata", "transcript_full.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	events, issues, e := Parse(bytes.NewReader(p), 0)
	if e != nil || len(events) != 2 || len(issues) != 1 || issues[0].Line != 2 {
		t.Fatalf("%#v %#v %v", events, issues, e)
	}
	if events[0].ID != "event-1" || events[0].ToolCalls[0].ID != "call-1" || events[0].Step != "1" || events[1].Step != "2" {
		t.Fatalf("events=%#v", events)
	}
	chunks, issues, e := Chunks(bytes.NewReader(p), Source{FilePath: "/x/brain/conversation-1/.system_generated/logs/transcript_full.jsonl", MachineID: "mac"})
	if e != nil || len(issues) != 1 || len(chunks) != 5 {
		t.Fatalf("%#v %#v %v", chunks, issues, e)
	}
	if chunks[0].SessionID != "conversation-1" || chunks[0].ProjectPath != "/antigravity/conversation-1" || !strings.Contains(chunks[0].Content, "Thinking:\nplan") || len(chunks[0].ChildChunkIDs) != 2 {
		t.Fatal(chunks[0])
	}
	if chunks[1].FilePath != "main.go" || chunks[2].FilePath != "main_test.go" || chunks[4].FilePath != "old.go" || chunks[4].Operation != "delete" {
		t.Fatalf("%#v %#v %#v", chunks[1], chunks[2], chunks[4])
	}
	wantTime := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	if !chunks[3].Timestamp.Equal(wantTime) || !chunks[4].Timestamp.Equal(wantTime) {
		t.Fatalf("expected propagated timestamp %v, got %v / %v", wantTime, chunks[3].Timestamp, chunks[4].Timestamp)
	}
	again, _, e := Chunks(bytes.NewReader(p), Source{FilePath: "/x/brain/conversation-1/.system_generated/logs/transcript_full.jsonl", MachineID: "mac"})
	if e != nil || again[0].ID != chunks[0].ID {
		t.Fatal(e)
	}
}

func TestBoundsBinaryAndHelpers(t *testing.T) {
	if _, _, e := Parse(nil, 1); e == nil {
		t.Fatal("nil")
	}
	if _, _, e := Parse(strings.NewReader("[]"), 1); e == nil {
		t.Fatal("size")
	}
	if _, _, e := Parse(strings.NewReader("[]"), MaxFileBytes+1); e == nil {
		t.Fatal("limit")
	}
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	if _, issues, e := Parse(strings.NewReader(deep), int64(len(deep)+1)); e != nil || len(issues) != 1 || issues[0].Reason != "json_too_deep" {
		t.Fatalf("depth: %#v %v", issues, e)
	}
	tooLongLine := strings.Repeat("a", MaxLineBytes+1) + "\n"
	if _, issues, e := Parse(strings.NewReader(tooLongLine), int64(len(tooLongLine)+1)); e != nil || len(issues) != 1 || issues[0].Reason != "line_too_long" {
		t.Fatalf("line_too_long: %#v %v", issues, e)
	}
	chunks, _, e := Chunks(strings.NewReader("\x00hello\x01world"), Source{FilePath: "/old.pb", Binary: true})
	if e != nil || len(chunks) != 1 || chunks[0].ChunkType != "antigravity_history" {
		t.Fatal(chunks, e)
	}
	if sessionID("/a/brain/x/log") != "x" || sessionID("/a/f.pb") != "f" {
		t.Fatal("session")
	}
	if len(patch("*** Delete File: x")) != 1 || len(shell("touch x")) != 1 {
		t.Fatal("ops")
	}
	if !time.Unix(0, 1000).UTC().Equal(time.Unix(0, 1000).UTC()) {
		t.Fatal("time")
	}
	if _, _, e := Parse(failing{}, 10); e == nil {
		t.Fatal("read")
	}
	if _, _, e := Chunks(strings.NewReader(""), Source{}); e == nil {
		t.Fatal("missing source")
	}
}

func TestAntigravityStepsToolArgsAndUTF8Split(t *testing.T) {
	lines := []string{
		`{"id":"s0","step_type":"USER_INPUT","step_index":0,"user_input":{"text":"Implement parser"}}`,
		`{"id":"s1","step_type":"PLANNER_RESPONSE","step_index":1,"timestamp":"2026-10-01T10:00:00Z","planner_response":{"content":"Planning changes"},"thought":"Check tool args","toolCalls":[{"id":"tc1","name":"edit_file","input":{"Path":"/work/a.go","Target":"oldVal","Replacement":"newVal"}},{"id":"tc2","name":"run_command","arguments":"{\"CommandLine\":\"touch /work/b.go\"}"}]}`,
		`{"id":"s2","step_type":"TOOL_RESPONSE","step_index":2,"tool_response":{"output":"ok"}}`,
		`{"id":"s3","step_type":"TASK_BOUNDARY","step_index":3,"task_status":"IN_PROGRESS","task_name":"Verification"}`,
		`{"id":"s4","step_type":"GENERIC","step_index":4,"generic":{"message":"generic note"}}`,
		`{"id":"s5","step_type":"SYSTEM_MESSAGE","step_index":5,"system_message":"system alert"}`,
		`{"id":"s6","step_type":"CHECKPOINT","step_index":6,"checkpoint":{"summary":"saved state"}}`,
		`{"id":"s7","step_type":"ERROR_MESSAGE","step_index":7,"error_message":{"error":"transient warning"}}`,
	}
	input := strings.Join(lines, "\n") + "\n"
	chunks, issues, err := Chunks(strings.NewReader(input), Source{FilePath: "/root/brain/conv-steps/transcript_full.jsonl", MachineID: "macmini"})
	if err != nil || len(issues) != 0 {
		t.Fatalf("err=%v issues=%#v", err, issues)
	}
	// 8 turn chunks + 2 file_change chunks from step 1 = 10 chunks
	if len(chunks) != 10 {
		t.Fatalf("expected 10 chunks, got %d: %#v", len(chunks), chunks)
	}
	wantStamp := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	for i, c := range chunks {
		if !c.Timestamp.Equal(wantStamp) {
			t.Fatalf("chunk[%d] timestamp=%v, want %v", i, c.Timestamp, wantStamp)
		}
	}
	if chunks[2].ChunkType != "file_change" || chunks[2].FilePath != "/work/a.go" || chunks[2].Operation != "edit" || !strings.Contains(chunks[2].Content, "oldVal") || !strings.Contains(chunks[2].Content, "newVal") {
		t.Fatalf("capitalized edit file_change=%#v", chunks[2])
	}
	if chunks[3].ChunkType != "file_change" || chunks[3].FilePath != "/work/b.go" || chunks[3].Operation != "write" {
		t.Fatalf("capitalized CommandLine file_change=%#v", chunks[3])
	}
	for _, wantSub := range []string{"Implement parser", "Planning changes", "ok", "Verification", "generic note", "system alert", "saved state", "transient warning"} {
		found := false
		for _, c := range chunks {
			if strings.Contains(c.Content, wantSub) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing step content %q in chunks", wantSub)
		}
	}

	longUTF8 := strings.Repeat("世", 2300) + "a" + strings.Repeat("世", 1000)
	parts := splitContent(longUTF8)
	if len(parts) < 2 {
		t.Fatal("expected split parts")
	}
	for i, part := range parts {
		if !utf8.ValidString(part) || len(part) > MaxChunkContentLength {
			t.Fatalf("part[%d] invalid utf8 or length=%d", i, len(part))
		}
	}
	longEvent := `{"id":"long-1","step_type":"USER_INPUT","content":"` + longUTF8 + `"}` + "\n"
	longChunks, _, err := Chunks(strings.NewReader(longEvent), Source{FilePath: "/a/f.jsonl"})
	if err != nil || len(longChunks) != 2 || longChunks[1].ParentChunkID == "" {
		t.Fatalf("longChunks=%#v err=%v", longChunks, err)
	}
}

func TestSummariesSQLiteBTreeAndWALOverlay(t *testing.T) {
	const pageSize = 512
	rootDir := t.TempDir()
	transcriptPath := filepath.Join(rootDir, "brain", "conv-target", ".system_generated", "logs", "transcript_full.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o700); err != nil {
		t.Fatal(err)
	}

	// Missing DB fallback
	title, projPath, projName := lookupSummaryMetadata(transcriptPath, "conv-target")
	if title != "Antigravity Session" || projPath != "/antigravity/conv-target" || projName != "Antigravity Session" {
		t.Fatalf("missing db fallback = %q %q %q", title, projPath, projName)
	}

	// Corrupt DB fallback
	dbPath := filepath.Join(rootDir, "conversation_summaries.db")
	if err := os.WriteFile(dbPath, []byte("not-a-sqlite-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gotTitle, gotPath, _ := lookupSummaryMetadata(transcriptPath, "conv-target"); gotTitle != "Antigravity Session" || gotPath != "/antigravity/conv-target" {
		t.Fatalf("corrupt db fallback = %q %q", gotTitle, gotPath)
	}

	// Build a valid 5-page SQLite 3 database with:
	// - Page 1: SQLite header + sqlite_master leaf page pointing conversation_summaries -> rootpage 2
	// - Page 2: Table interior page (0x05) with left child = Page 3 and right-most child = Page 4
	// - Page 3: Table leaf page (0x0d) with a record that overflows onto Page 5
	// - Page 4: Table leaf page (0x0d) with target session "conv-target"
	// - Page 5: Overflow page for Page 3's record
	page1 := make([]byte, pageSize)
	copy(page1[:16], "SQLite format 3\x00")
	binary.BigEndian.PutUint16(page1[16:18], pageSize)
	page1[18], page1[19], page1[20] = 1, 1, 0
	masterRecord := buildSQLiteRecord([]any{"table", "conversation_summaries", "conversation_summaries", int64(2), "CREATE TABLE conversation_summaries (...)"})
	writeLeafPage(page1, 100, []leafCell{{rowID: 1, payload: masterRecord}})

	page2 := make([]byte, pageSize)
	page2[0] = 0x05 // interior table b-tree page
	binary.BigEndian.PutUint16(page2[3:5], 1)
	binary.BigEndian.PutUint32(page2[8:12], 4) // right-most child = page 4
	cellOff := 400
	binary.BigEndian.PutUint16(page2[12:14], uint16(cellOff))
	binary.BigEndian.PutUint32(page2[cellOff:cellOff+4], 3) // left child = page 3
	page2[cellOff+4] = 1                                    // rowid = 1

	// Page 3 + Page 5: Leaf cell with overflow payload (> maxLocal = 512 - 35 = 477)
	page3 := make([]byte, pageSize)
	page5 := make([]byte, pageSize)
	longSummary := strings.Repeat("overflow-data-", 40) // > 500 bytes
	ovRecord := buildSQLiteRecord([]any{"conv-overflow", "Overflow Title", longSummary, int64(1), int64(2), `["file:///Volumes/1TB_USB/OverflowRepo"]`, int64(0)})
	writeOverflowLeafCell(page3, page5, 5, 1, ovRecord)

	// Page 4: Leaf cell with "conv-target"
	page4 := make([]byte, pageSize)
	baseRecord := buildSQLiteRecord([]any{"conv-target", "Initial DB Title", "summary", int64(10), int64(20), `["file:///Volumes/1TB_USB/Meridian"]`, int64(1)})
	writeLeafPage(page4, 0, []leafCell{{rowID: 2, payload: baseRecord}})

	dbBytes := bytes.Join([][]byte{page1, page2, page3, page4, page5}, nil)
	if err := os.WriteFile(dbPath, dbBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Verify overflow record on Page 3+5 is readable
	ovTitle, ovPath, ovName := lookupSummaryMetadata(transcriptPath, "conv-overflow")
	if ovTitle != "Overflow Title" || ovPath != "/Volumes/1TB_USB/OverflowRepo" || ovName != "OverflowRepo" {
		t.Fatalf("overflow lookup = %q %q %q", ovTitle, ovPath, ovName)
	}

	// Verify target record on Page 4 (via interior Page 2) is readable
	title, projPath, projName = lookupSummaryMetadata(transcriptPath, "conv-target")
	if title != "Initial DB Title" || projPath != "/Volumes/1TB_USB/Meridian" || projName != "Meridian" {
		t.Fatalf("base db lookup = %q %q %q", title, projPath, projName)
	}

	// Now write a WAL file with:
	// - Frame 1 (committed, dbSize=5): overrides Page 4 with "WAL Committed Title" and workspace "/Volumes/1TB_USB/WalRepo"
	// - Frame 2 (uncommitted, dbSize=0): overrides Page 4 with "WAL Uncommitted Title" (must be ignored!)
	// - Trailing truncated bytes (must be ignored!)
	walPage4Committed := make([]byte, pageSize)
	walRecordCommitted := buildSQLiteRecord([]any{"conv-target", "WAL Committed Title", "summary", int64(10), int64(30), "file:///Volumes/1TB_USB/WalRepo", int64(1)})
	writeLeafPage(walPage4Committed, 0, []leafCell{{rowID: 2, payload: walRecordCommitted}})

	walPage4Uncommitted := make([]byte, pageSize)
	walRecordUncommitted := buildSQLiteRecord([]any{"conv-target", "WAL Uncommitted Title", "summary", int64(10), int64(40), "file:///Volumes/1TB_USB/BadRepo", int64(1)})
	writeLeafPage(walPage4Uncommitted, 0, []leafCell{{rowID: 2, payload: walRecordUncommitted}})

	walBytes := buildTestWAL(pageSize, false, []walFrameSpec{
		{pageNo: 4, dbSize: 5, data: walPage4Committed},
		{pageNo: 4, dbSize: 0, data: walPage4Uncommitted},
	})
	walBytes = append(walBytes, []byte("truncated-frame")...)
	if err := os.WriteFile(dbPath+"-wal", walBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	title, projPath, projName = lookupSummaryMetadata(transcriptPath, "conv-target")
	if title != "WAL Committed Title" || projPath != "/Volumes/1TB_USB/WalRepo" || projName != "WalRepo" {
		t.Fatalf("wal committed lookup = %q %q %q", title, projPath, projName)
	}

	// Verify big-endian WAL magic (0x377f0683) also works and Chunks() integrates the metadata
	walBE := buildTestWAL(pageSize, true, []walFrameSpec{
		{pageNo: 4, dbSize: 5, data: walPage4Committed},
	})
	if err := os.WriteFile(dbPath+"-wal", walBE, 0o600); err != nil {
		t.Fatal(err)
	}
	chunks, _, err := Chunks(strings.NewReader(`{"id":"e1","type":"USER_INPUT","created_at":"2026-10-01T10:00:00Z","content":"hello"}`+"\n"), Source{FilePath: transcriptPath, MachineID: "mac"})
	if err != nil || len(chunks) != 1 || chunks[0].ProjectPath != "/Volumes/1TB_USB/WalRepo" || chunks[0].ProjectName != "WalRepo" || !strings.Contains(chunks[0].Content, "Title: WAL Committed Title") {
		t.Fatalf("Chunks with SQLite+WAL metadata = %#v, err=%v", chunks, err)
	}
	if v, n := readVarint([]byte{0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x01}); n != 9 || v == 0 {
		t.Fatalf("9-byte varint = %d %d", v, n)
	}
	if _, _, ok := decodeSerialValue(7, make([]byte, 8)); !ok {
		t.Fatal("float64 serial")
	}
	if val, n, ok := decodeSerialValue(16, []byte("ab")); !ok || n != 2 || val != "ab" {
		t.Fatalf("blob serial = %v %d %v", val, n, ok)
	}
}

type leafCell struct {
	rowID   uint64
	payload []byte
}

func writeLeafPage(page []byte, hdrOff int, cells []leafCell) {
	page[hdrOff] = 0x0d
	binary.BigEndian.PutUint16(page[hdrOff+3:hdrOff+5], uint16(len(cells)))
	writePos := len(page)
	for i, c := range cells {
		var hdr []byte
		hdr = appendVarint(hdr, uint64(len(c.payload)))
		hdr = appendVarint(hdr, c.rowID)
		cellBytes := append(hdr, c.payload...)
		writePos -= len(cellBytes)
		copy(page[writePos:], cellBytes)
		ptrOff := hdrOff + 8 + i*2
		binary.BigEndian.PutUint16(page[ptrOff:ptrOff+2], uint16(writePos))
	}
}

func writeOverflowLeafCell(leafPage, ovPage []byte, ovPageNo uint32, rowID uint64, payload []byte) {
	pageSize := len(leafPage)
	maxLocal := pageSize - 35
	minLocal := ((pageSize-12)*32/255) - 23
	localSize := minLocal + ((len(payload) - minLocal) % (pageSize - 4))
	if localSize > maxLocal {
		localSize = minLocal
	}
	leafPage[0] = 0x0d
	binary.BigEndian.PutUint16(leafPage[3:5], 1)
	var hdr []byte
	hdr = appendVarint(hdr, uint64(len(payload)))
	hdr = appendVarint(hdr, rowID)
	cellSize := len(hdr) + localSize + 4
	cellOff := pageSize - cellSize
	binary.BigEndian.PutUint16(leafPage[8:10], uint16(cellOff))
	copy(leafPage[cellOff:], hdr)
	copy(leafPage[cellOff+len(hdr):], payload[:localSize])
	binary.BigEndian.PutUint32(leafPage[cellOff+len(hdr)+localSize:], ovPageNo)

	binary.BigEndian.PutUint32(ovPage[0:4], 0)
	copy(ovPage[4:], payload[localSize:])
}

func buildSQLiteRecord(cols []any) []byte {
	var serials []uint64
	var body []byte
	for _, col := range cols {
		switch v := col.(type) {
		case nil:
			serials = append(serials, 0)
		case int64:
			if v == 0 {
				serials = append(serials, 8)
			} else if v == 1 {
				serials = append(serials, 9)
			} else if v >= -128 && v <= 127 {
				serials = append(serials, 1)
				body = append(body, byte(int8(v)))
			} else {
				serials = append(serials, 2)
				var b [2]byte
				binary.BigEndian.PutUint16(b[:], uint16(int16(v)))
				body = append(body, b[:]...)
			}
		case string:
			serials = append(serials, uint64(len(v)*2+13))
			body = append(body, v...)
		}
	}
	var serialBytes []byte
	for _, s := range serials {
		serialBytes = appendVarint(serialBytes, s)
	}
	hdrSize := uint64(len(serialBytes) + 1)
	var hdr []byte
	hdr = appendVarint(hdr, hdrSize)
	hdr = append(hdr, serialBytes...)
	return append(hdr, body...)
}

func appendVarint(dst []byte, v uint64) []byte {
	if v < 0x80 {
		return append(dst, byte(v))
	}
	var buf [9]byte
	n := 0
	for tmp := v; tmp > 0; tmp >>= 7 {
		n++
	}
	for i := n - 1; i >= 0; i-- {
		b := byte((v >> (uint(i) * 7)) & 0x7f)
		if i > 0 {
			b |= 0x80
		}
		buf[n-1-i] = b
	}
	return append(dst, buf[:n]...)
}

type walFrameSpec struct {
	pageNo uint32
	dbSize uint32
	data   []byte
}

func buildTestWAL(pageSize int, bigEndian bool, frames []walFrameSpec) []byte {
	var hdr [32]byte
	if bigEndian {
		binary.BigEndian.PutUint32(hdr[0:4], 0x377f0683)
	} else {
		binary.BigEndian.PutUint32(hdr[0:4], 0x377f0682)
	}
	binary.BigEndian.PutUint32(hdr[4:8], 3007000)
	binary.BigEndian.PutUint32(hdr[8:12], uint32(pageSize))
	binary.BigEndian.PutUint32(hdr[16:20], 0x11223344)
	binary.BigEndian.PutUint32(hdr[20:24], 0x55667788)
	s1, s2 := walChecksum(hdr[0:24], 0, 0, bigEndian)
	binary.BigEndian.PutUint32(hdr[24:28], s1)
	binary.BigEndian.PutUint32(hdr[28:32], s2)

	out := append([]byte(nil), hdr[:]...)
	for _, f := range frames {
		var fh [24]byte
		binary.BigEndian.PutUint32(fh[0:4], f.pageNo)
		binary.BigEndian.PutUint32(fh[4:8], f.dbSize)
		binary.BigEndian.PutUint32(fh[8:12], 0x11223344)
		binary.BigEndian.PutUint32(fh[12:16], 0x55667788)
		s1, s2 = walChecksum(fh[0:8], s1, s2, bigEndian)
		s1, s2 = walChecksum(f.data, s1, s2, bigEndian)
		binary.BigEndian.PutUint32(fh[16:20], s1)
		binary.BigEndian.PutUint32(fh[20:24], s2)
		out = append(out, fh[:]...)
		out = append(out, f.data...)
	}
	return out
}

type failing struct{}

func (failing) Read([]byte) (int, error) { return 0, errors.New("x") }
