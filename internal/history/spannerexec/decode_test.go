package spannerexec

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestDecodeValueSupportsHistoryColumnTypes(t *testing.T) {
	timestamp := time.Date(2026, time.September, 9, 12, 34, 56, 789000000, time.UTC)
	for name, test := range map[string]struct {
		value spanner.GenericColumnValue
		want  any
	}{
		"string": {
			value: genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("chunk")),
			want:  "chunk",
		},
		"int64": {
			value: genericColumn(sppb.TypeCode_INT64, structpb.NewStringValue("42")),
			want:  int64(42),
		},
		"bool": {
			value: genericColumn(sppb.TypeCode_BOOL, structpb.NewBoolValue(true)),
			want:  true,
		},
		"float64": {
			value: genericColumn(sppb.TypeCode_FLOAT64, structpb.NewNumberValue(0.75)),
			want:  0.75,
		},
		"timestamp": {
			value: genericColumn(sppb.TypeCode_TIMESTAMP, structpb.NewStringValue(timestamp.Format(time.RFC3339Nano))),
			want:  timestamp,
		},
		"bytes": {
			value: genericColumn(sppb.TypeCode_BYTES, structpb.NewStringValue(base64.StdEncoding.EncodeToString([]byte("receipt")))),
			want:  []byte("receipt"),
		},
		"string array": {
			value: genericArray(sppb.TypeCode_STRING, structpb.NewStringValue("first"), structpb.NewStringValue("second")),
			want:  []string{"first", "second"},
		},
		"float64 array": {
			value: genericArray(sppb.TypeCode_FLOAT64, structpb.NewNumberValue(0.25), structpb.NewNumberValue(0.5)),
			want:  []float64{0.25, 0.5},
		},
		"query embedding float32 array": {
			value: genericArray(sppb.TypeCode_FLOAT32, structpb.NewNumberValue(0.25), structpb.NewNumberValue(0.5)),
			want:  []float32{0.25, 0.5},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeValue(test.value)
			if err != nil {
				t.Fatalf("decodeValue() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("decodeValue() = %#v (%T), want %#v (%T)", got, got, test.want, test.want)
			}
		})
	}
}

func TestDecodeValueNullSupportedValuesBecomeNil(t *testing.T) {
	for name, value := range map[string]spanner.GenericColumnValue{
		"scalar": genericColumn(sppb.TypeCode_STRING, structpb.NewNullValue()),
		"array": {Type: &sppb.Type{
			Code:             sppb.TypeCode_ARRAY,
			ArrayElementType: &sppb.Type{Code: sppb.TypeCode_FLOAT64},
		}, Value: structpb.NewNullValue()},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeValue(value)
			if err != nil || got != nil {
				t.Fatalf("decodeValue() = %#v, %v; want nil, nil", got, err)
			}
		})
	}
}

func TestDecodeValueRejectsMalformedAndUnsupportedColumns(t *testing.T) {
	unsupported := genericColumn(sppb.TypeCode_DATE, structpb.NewStringValue("2026-09-09"))
	unsupportedNull := genericColumn(sppb.TypeCode_JSON, structpb.NewNullValue())
	invalidArray := spanner.GenericColumnValue{Type: &sppb.Type{Code: sppb.TypeCode_ARRAY}, Value: structpb.NewListValue(&structpb.ListValue{})}
	for name, value := range map[string]spanner.GenericColumnValue{
		"missing type":          {Value: structpb.NewStringValue("x")},
		"missing value":         {Type: &sppb.Type{Code: sppb.TypeCode_STRING}},
		"unsupported":           unsupported,
		"unsupported null":      unsupportedNull,
		"array without element": invalidArray,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeValue(value); err == nil {
				t.Fatal("decodeValue() accepted invalid column")
			}
		})
	}
}

func TestDecodeValueReportsDecodeFailureAndDecodeRowStops(t *testing.T) {
	malformed := genericColumn(sppb.TypeCode_INT64, structpb.NewStringValue("not-an-integer"))
	if _, err := decodeValue(malformed); err == nil || !strings.Contains(err.Error(), "INT64") {
		t.Fatalf("decodeValue() error = %v, want an INT64 decode error", err)
	}
	if row, err := decodeRow([]spanner.GenericColumnValue{genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("ok")), malformed}); err == nil || row != nil {
		t.Fatalf("decodeRow() = %#v, %v; want nil row and decode error", row, err)
	}
}

func TestDecodeRowPreservesColumnOrder(t *testing.T) {
	row, err := decodeRow([]spanner.GenericColumnValue{
		genericColumn(sppb.TypeCode_STRING, structpb.NewStringValue("chunk")),
		genericColumn(sppb.TypeCode_INT64, structpb.NewStringValue("7")),
		genericColumn(sppb.TypeCode_BOOL, structpb.NewBoolValue(false)),
	})
	if err != nil {
		t.Fatalf("decodeRow() error = %v", err)
	}
	want := []any{"chunk", int64(7), false}
	if !reflect.DeepEqual([]any(row), want) {
		t.Fatalf("decodeRow() = %#v, want %#v", row, want)
	}
}

func genericColumn(code sppb.TypeCode, value *structpb.Value) spanner.GenericColumnValue {
	return spanner.GenericColumnValue{Type: &sppb.Type{Code: code}, Value: value}
}

func genericArray(element sppb.TypeCode, values ...*structpb.Value) spanner.GenericColumnValue {
	return spanner.GenericColumnValue{
		Type:  &sppb.Type{Code: sppb.TypeCode_ARRAY, ArrayElementType: &sppb.Type{Code: element}},
		Value: structpb.NewListValue(&structpb.ListValue{Values: values}),
	}
}
