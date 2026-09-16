package spannerexec

import (
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"google.golang.org/protobuf/types/known/structpb"
)

// decodeValue converts the closed set of values emitted by the history schema.
func decodeValue(value spanner.GenericColumnValue) (any, error) {
	if value.Type == nil || value.Value == nil {
		return nil, fmt.Errorf("decode Spanner column: missing type or value")
	}
	if !supportedColumnType(value.Type) {
		return nil, fmt.Errorf("decode unsupported Spanner column type %s", value.Type.Code)
	}
	if _, isNull := value.Value.Kind.(*structpb.Value_NullValue); isNull {
		return nil, nil
	}

	var target any
	switch value.Type.Code {
	case sppb.TypeCode_STRING:
		target = new(string)
	case sppb.TypeCode_INT64:
		target = new(int64)
	case sppb.TypeCode_BOOL:
		target = new(bool)
	case sppb.TypeCode_FLOAT64:
		target = new(float64)
	case sppb.TypeCode_TIMESTAMP:
		target = new(time.Time)
	case sppb.TypeCode_BYTES:
		target = new([]byte)
	case sppb.TypeCode_ARRAY:
		switch value.Type.ArrayElementType.Code {
		case sppb.TypeCode_STRING:
			target = new([]string)
		case sppb.TypeCode_FLOAT64:
			target = new([]float64)
		case sppb.TypeCode_FLOAT32:
			target = new([]float32)
		}
	}
	if target == nil {
		return nil, fmt.Errorf("decode unsupported Spanner column type %s", value.Type)
	}
	if err := value.Decode(target); err != nil {
		return nil, fmt.Errorf("decode Spanner column type %s: %w", value.Type, err)
	}
	switch typed := target.(type) {
	case *string:
		return *typed, nil
	case *int64:
		return *typed, nil
	case *bool:
		return *typed, nil
	case *float64:
		return *typed, nil
	case *time.Time:
		return *typed, nil
	case *[]byte:
		return *typed, nil
	case *[]string:
		return *typed, nil
	case *[]float64:
		return *typed, nil
	case *[]float32:
		return *typed, nil
	default:
		return nil, fmt.Errorf("decode Spanner column type %s produced unsupported destination %T", value.Type, target)
	}
}

func supportedColumnType(columnType *sppb.Type) bool {
	switch columnType.Code {
	case sppb.TypeCode_STRING, sppb.TypeCode_INT64, sppb.TypeCode_BOOL, sppb.TypeCode_FLOAT64, sppb.TypeCode_TIMESTAMP, sppb.TypeCode_BYTES:
		return true
	case sppb.TypeCode_ARRAY:
		return columnType.ArrayElementType != nil && (columnType.ArrayElementType.Code == sppb.TypeCode_STRING || columnType.ArrayElementType.Code == sppb.TypeCode_FLOAT64 || columnType.ArrayElementType.Code == sppb.TypeCode_FLOAT32)
	default:
		return false
	}
}

func decodeRow(values []spanner.GenericColumnValue) (store.Row, error) {
	row := make(store.Row, len(values))
	for i, value := range values {
		decoded, err := decodeValue(value)
		if err != nil {
			return nil, err
		}
		row[i] = decoded
	}
	return row, nil
}
