package antigravity

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxSQLiteBytes = 64 << 20
	maxBTreePages  = 4096
)

func lookupSummaryMetadata(transcriptFilePath, sessionID string) (string, string, string) {
	defaultTitle, defaultPath, defaultName := "Antigravity Session", "/antigravity/"+sessionID, "Antigravity Session"
	dbPath := summaryDBPath(transcriptFilePath)
	if dbPath == "" || sessionID == "" {
		return defaultTitle, defaultPath, defaultName
	}
	dbData, ok := readRegularFileBounded(dbPath, maxSQLiteBytes)
	if !ok || len(dbData) < 100 || string(dbData[:16]) != "SQLite format 3\x00" {
		return defaultTitle, defaultPath, defaultName
	}
	pageSize := int(binary.BigEndian.Uint16(dbData[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return defaultTitle, defaultPath, defaultName
	}
	usableSize := pageSize - int(dbData[20])
	if usableSize < 480 {
		return defaultTitle, defaultPath, defaultName
	}
	overlay := readCommittedWALOverlay(dbPath+"-wal", pageSize)
	db := &sqliteReader{data: dbData, pageSize: pageSize, usableSize: usableSize, overlay: overlay}

	rootPage := db.findTableRootPage("conversation_summaries")
	if rootPage == 0 {
		return defaultTitle, defaultPath, defaultName
	}
	var foundTitle, foundURIs string
	matched := false
	db.scanTable(rootPage, func(cols []any) bool {
		if len(cols) >= 6 && text(cols[0]) == sessionID {
			foundTitle = strings.TrimSpace(text(cols[1]))
			foundURIs = strings.TrimSpace(text(cols[5]))
			matched = true
			return true
		}
		return false
	})
	if !matched {
		return defaultTitle, defaultPath, defaultName
	}
	title := first(foundTitle, defaultTitle)
	if projPath, projName := parseWorkspaceURI(foundURIs); projPath != "" {
		return title, projPath, projName
	}
	return title, defaultPath, defaultName
}

func summaryDBPath(transcriptFilePath string) string {
	clean := filepath.Clean(transcriptFilePath)
	for dir := clean; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		if filepath.Base(parent) == "brain" {
			root := filepath.Dir(parent)
			if root == parent || root == "." {
				return ""
			}
			return filepath.Join(root, "conversation_summaries.db")
		}
		dir = parent
	}
}

func readRegularFileBounded(path string, maxBytes int64) ([]byte, bool) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, false
	}
	return data, true
}

func readCommittedWALOverlay(walPath string, pageSize int) map[uint32][]byte {
	walData, ok := readRegularFileBounded(walPath, maxSQLiteBytes)
	if !ok || len(walData) < 32 {
		return nil
	}
	magic := binary.BigEndian.Uint32(walData[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil
	}
	bigEndian := magic == 0x377f0683
	if int(binary.BigEndian.Uint32(walData[8:12])) != pageSize {
		return nil
	}
	salt1, salt2 := binary.BigEndian.Uint32(walData[16:20]), binary.BigEndian.Uint32(walData[20:24])
	s1, s2 := walChecksum(walData[0:24], 0, 0, bigEndian)
	if s1 != binary.BigEndian.Uint32(walData[24:28]) || s2 != binary.BigEndian.Uint32(walData[28:32]) {
		return nil
	}
	committed := make(map[uint32][]byte)
	pending := make(map[uint32][]byte)
	frameSize := 24 + pageSize
	for offset := 32; offset+frameSize <= len(walData); offset += frameSize {
		fh := walData[offset : offset+24]
		fp := walData[offset+24 : offset+frameSize]
		pageNo := binary.BigEndian.Uint32(fh[0:4])
		dbSize := binary.BigEndian.Uint32(fh[4:8])
		if pageNo == 0 || binary.BigEndian.Uint32(fh[8:12]) != salt1 || binary.BigEndian.Uint32(fh[12:16]) != salt2 {
			break
		}
		s1, s2 = walChecksum(fh[0:8], s1, s2, bigEndian)
		s1, s2 = walChecksum(fp, s1, s2, bigEndian)
		if s1 != binary.BigEndian.Uint32(fh[16:20]) || s2 != binary.BigEndian.Uint32(fh[20:24]) {
			break
		}
		pending[pageNo] = fp
		if dbSize > 0 {
			for k, v := range pending {
				committed[k] = v
			}
			clear(pending)
		}
	}
	return committed
}

func walChecksum(data []byte, s1, s2 uint32, bigEndian bool) (uint32, uint32) {
	for i := 0; i+8 <= len(data); i += 8 {
		var x0, x1 uint32
		if bigEndian {
			x0 = binary.BigEndian.Uint32(data[i : i+4])
			x1 = binary.BigEndian.Uint32(data[i+4 : i+8])
		} else {
			x0 = binary.LittleEndian.Uint32(data[i : i+4])
			x1 = binary.LittleEndian.Uint32(data[i+4 : i+8])
		}
		s1 += x0 + s2
		s2 += x1 + s1
	}
	return s1, s2
}

type sqliteReader struct {
	data       []byte
	pageSize   int
	usableSize int
	overlay    map[uint32][]byte
}

func (r *sqliteReader) page(pageNo uint32) ([]byte, bool) {
	if pageNo == 0 {
		return nil, false
	}
	if p, ok := r.overlay[pageNo]; ok && len(p) >= r.usableSize {
		return p, true
	}
	start := int(pageNo-1) * r.pageSize
	end := start + r.pageSize
	if start < 0 || end > len(r.data) {
		return nil, false
	}
	return r.data[start:end], true
}

func (r *sqliteReader) findTableRootPage(tableName string) uint32 {
	var root uint32
	r.scanTable(1, func(cols []any) bool {
		if len(cols) >= 4 && text(cols[0]) == "table" && (text(cols[1]) == tableName || text(cols[2]) == tableName) {
			if n, ok := cols[3].(int64); ok && n > 0 {
				root = uint32(n)
				return true
			}
		}
		return false
	})
	return root
}

func (r *sqliteReader) scanTable(rootPage uint32, visit func([]any) bool) {
	stack := []uint32{rootPage}
	visited := make(map[uint32]bool)
	for len(stack) > 0 && len(visited) < maxBTreePages {
		pageNo := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pageNo == 0 || visited[pageNo] {
			continue
		}
		visited[pageNo] = true
		page, ok := r.page(pageNo)
		if !ok {
			continue
		}
		hdrOff := 0
		if pageNo == 1 {
			hdrOff = 100
		}
		if hdrOff+8 > r.usableSize {
			continue
		}
		pageType := page[hdrOff]
		cellCount := int(binary.BigEndian.Uint16(page[hdrOff+3 : hdrOff+5]))
		switch pageType {
		case 0x05:
			if hdrOff+12+cellCount*2 > r.usableSize {
				continue
			}
			rightChild := binary.BigEndian.Uint32(page[hdrOff+8 : hdrOff+12])
			if rightChild != 0 {
				stack = append(stack, rightChild)
			}
			for i := cellCount - 1; i >= 0; i-- {
				ptrOff := hdrOff + 12 + i*2
				cellOff := int(binary.BigEndian.Uint16(page[ptrOff : ptrOff+2]))
				if cellOff+4 <= r.usableSize {
					stack = append(stack, binary.BigEndian.Uint32(page[cellOff:cellOff+4]))
				}
			}
		case 0x0d:
			if hdrOff+8+cellCount*2 > r.usableSize {
				continue
			}
			for i := 0; i < cellCount; i++ {
				ptrOff := hdrOff + 8 + i*2
				cellOff := int(binary.BigEndian.Uint16(page[ptrOff : ptrOff+2]))
				payload, ok := r.readLeafCellPayload(page, cellOff)
				if !ok {
					continue
				}
				if cols, ok := parseSQLiteRecord(payload); ok && visit(cols) {
					return
				}
			}
		}
	}
}

func (r *sqliteReader) readLeafCellPayload(page []byte, cellOff int) ([]byte, bool) {
	if cellOff >= r.usableSize {
		return nil, false
	}
	payloadSize64, n1 := readVarint(page[cellOff:r.usableSize])
	if n1 == 0 {
		return nil, false
	}
	_, n2 := readVarint(page[cellOff+n1 : r.usableSize])
	if n2 == 0 {
		return nil, false
	}
	pos := cellOff + n1 + n2
	payloadSize := int(payloadSize64)
	if payloadSize < 0 || payloadSize > maxSQLiteBytes {
		return nil, false
	}
	maxLocal := r.usableSize - 35
	if payloadSize <= maxLocal {
		if pos+payloadSize > r.usableSize {
			return nil, false
		}
		return page[pos : pos+payloadSize], true
	}
	minLocal := ((r.usableSize-12)*32/255) - 23
	localSize := minLocal + ((payloadSize - minLocal) % (r.usableSize - 4))
	if localSize > maxLocal {
		localSize = minLocal
	}
	if pos+localSize+4 > r.usableSize {
		return nil, false
	}
	buf := make([]byte, 0, payloadSize)
	buf = append(buf, page[pos:pos+localSize]...)
	ovPageNo := binary.BigEndian.Uint32(page[pos+localSize : pos+localSize+4])
	for steps := 0; len(buf) < payloadSize && ovPageNo != 0 && steps < maxBTreePages; steps++ {
		ovPage, ok := r.page(ovPageNo)
		if !ok || r.usableSize < 4 {
			return nil, false
		}
		nextPage := binary.BigEndian.Uint32(ovPage[0:4])
		chunk := ovPage[4:r.usableSize]
		need := payloadSize - len(buf)
		if len(chunk) > need {
			chunk = chunk[:need]
		}
		buf = append(buf, chunk...)
		ovPageNo = nextPage
	}
	return buf, len(buf) == payloadSize
}

var sqliteIntSizes = [10]int{0, 1, 2, 3, 4, 6, 8, 8, 0, 0}

func parseSQLiteRecord(payload []byte) ([]any, bool) {
	hdrSize64, pos := readVarint(payload)
	hdrSize := int(hdrSize64)
	if pos == 0 || hdrSize < pos || hdrSize > len(payload) {
		return nil, false
	}
	var serials []uint64
	for pos < hdrSize {
		serial, n := readVarint(payload[pos:hdrSize])
		if n == 0 {
			return nil, false
		}
		serials = append(serials, serial)
		pos += n
	}
	bodyPos := hdrSize
	cols := make([]any, len(serials))
	for i, serial := range serials {
		val, n, ok := decodeSerialValue(serial, payload[bodyPos:])
		if !ok {
			return nil, false
		}
		cols[i] = val
		bodyPos += n
	}
	return cols, true
}

func decodeSerialValue(serial uint64, data []byte) (any, int, bool) {
	if serial < 10 {
		n := sqliteIntSizes[serial]
		if len(data) < n {
			return nil, 0, false
		}
		if serial == 0 || serial == 7 {
			return nil, n, true
		}
		if serial == 8 || serial == 9 {
			return int64(serial - 8), 0, true
		}
		v := int64(int8(data[0]))
		for i := 1; i < n; i++ {
			v = (v << 8) | int64(data[i])
		}
		return v, n, true
	}
	if serial < 12 {
		return nil, 0, false
	}
	n := int(serial-12) / 2
	if len(data) < n {
		return nil, 0, false
	}
	return string(data[:n]), n, true
}

func readVarint(buf []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(buf) && i < 9; i++ {
		b := buf[i]
		if i == 8 {
			v = (v << 8) | uint64(b)
			return v, 9
		}
		v = (v << 7) | uint64(b&0x7f)
		if b&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func parseWorkspaceURI(raw string) (string, string) {
	if raw == "" {
		return "", ""
	}
	var candidates []string
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		candidates = []string{raw}
	}
	for _, candidate := range candidates {
		u, err := url.Parse(strings.TrimSpace(candidate))
		if err != nil || u.Scheme != "file" || u.Path == "" {
			continue
		}
		cleaned := filepath.Clean(u.Path)
		if cleaned != "" && cleaned != "." && cleaned != "/" {
			return cleaned, filepath.Base(cleaned)
		}
	}
	return "", ""
}
