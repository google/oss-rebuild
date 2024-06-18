package form

import (
	"net/url"
	"reflect"
	"testing"
)

type TestStruct struct {
	StringField   string   `form:"string_field,required"`
	IntField      int      `form:"int_field"`
	SliceField    []string `form:"slice_field"`
	IntSliceField []int    `form:""`
}

func TestMarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    url.Values
		wantErr bool
	}{
		{
			name: "Valid struct",
			input: TestStruct{
				StringField:   "test",
				IntField:      123,
				SliceField:    []string{"a", "b", "c"},
				IntSliceField: []int{1, 2, 3},
			},
			want: url.Values{
				"string_field":  []string{"test"},
				"int_field":     []string{"123"},
				"slice_field":   []string{"a", "b", "c"},
				"intslicefield": []string{"[1,2,3]"},
			},
			wantErr: false,
		},
		{
			name:    "Not a struct",
			input:   "not a struct",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Pointer not a struct",
			input:   &TestStruct{},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Marshal(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Marshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Marshal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   url.Values
		want    TestStruct
		wantErr bool
	}{
		{
			name: "Valid input",
			input: url.Values{
				"string_field": {"test"},
				"int_field":    {"123"},
				"slice_field":  {"a", "b", "c"},
			},
			want: TestStruct{
				StringField: "test",
				IntField:    123,
				SliceField:  []string{"a", "b", "c"},
			},
			wantErr: false,
		},
		{
			name: "Missing required field",
			input: url.Values{
				"int_field":   {"123"},
				"slice_field": {"a", "b", "c"},
			},
			want:    TestStruct{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got TestStruct
			err := Unmarshal(tt.input, &got)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptions(t *testing.T) {
	type testStruct struct {
		Field1 string `form:"custom_name,required"`
		Field2 string
		Field3 string `form:",required"`
	}

	tests := []struct {
		name  string
		field reflect.StructField
		want  fieldOptions
	}{
		{
			name:  "Custom name and required",
			field: reflect.TypeOf(testStruct{}).Field(0),
			want:  fieldOptions{name: "custom_name", required: true},
		},
		{
			name:  "Default name",
			field: reflect.TypeOf(testStruct{}).Field(1),
			want:  fieldOptions{name: "field2", required: false},
		},
		{
			name:  "Default name required",
			field: reflect.TypeOf(testStruct{}).Field(2),
			want:  fieldOptions{name: "field3", required: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := options(tt.field)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("options() = %v, want %v", got, tt.want)
			}
		})
	}
}
