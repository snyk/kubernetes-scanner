package kubeobjects_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/snyk/kubernetes-scanner/internal/kubeobjects"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRemoveAttributes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		input, expected map[string]interface{}
		expr            string
	}{
		{
			name: "empty removal expression is a no-op",
			input: map[string]interface{}{
				"foo": "bar",
			},
			expected: map[string]interface{}{
				"foo": "bar",
			},
		},
		{
			name: "removes scalar fields",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": "barry",
			},
			expected: map[string]interface{}{
				"foo": "bar",
			},
			expr: "baz",
		},
		{
			name: "removes object fields",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"one": 1,
				},
			},
			expected: map[string]interface{}{
				"foo": "bar",
			},
			expr: "baz",
		},
		{
			name: "removes array fields",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": []int{1, 2},
			},
			expected: map[string]interface{}{
				"foo": "bar",
			},
			expr: "baz",
		},
		{
			name: "removes fields nested in objects",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"barry": map[string]interface{}{
						"field1": float64(1),
						"field2": float64(2),
						"field3": float64(3),
					},
				},
			},
			expected: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"barry": map[string]interface{}{
						"field1": float64(1),
						"field3": float64(3),
					},
				},
			},
			expr: "baz.barry.field2",
		},
		{
			name: "removes fields nested in arrays",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"arr": []interface{}{
						map[string]interface{}{
							"field1": float64(1),
							"field2": float64(2),
							"field3": float64(3),
						},
						map[string]interface{}{
							"field1": float64(1),
							"field2": float64(2),
							"field3": float64(3),
						},
					},
				},
			},
			expected: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"arr": []interface{}{
						map[string]interface{}{
							"field1": float64(1),
							"field3": float64(3),
						},
						map[string]interface{}{
							"field1": float64(1),
							"field3": float64(3),
						},
					},
				},
			},
			expr: "baz.arr.field2",
		},
		{
			name: "removes fields nested in arrays when there are scalar values present in a mixed-type array",
			input: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"arr": []interface{}{
						map[string]interface{}{
							"field1": float64(1),
							"field2": float64(2),
							"field3": float64(3),
						},
						"a-string",
						map[string]interface{}{
							"field1": float64(1),
							"field2": float64(2),
							"field3": float64(3),
						},
					},
				},
			},
			expected: map[string]interface{}{
				"foo": "bar",
				"baz": map[string]interface{}{
					"arr": []interface{}{
						map[string]interface{}{
							"field1": float64(1),
							"field3": float64(3),
						},
						"a-string",
						map[string]interface{}{
							"field1": float64(1),
							"field3": float64(3),
						},
					},
				},
			},
			expr: "baz.arr.field2",
		},
		{
			name: "non-existent field removal is a no-op",
			input: map[string]interface{}{
				"foo": map[string]interface{}{
					"bar": "baz",
				},
			},
			expected: map[string]interface{}{
				"foo": map[string]interface{}{
					"bar": "baz",
				},
			},
			expr: "foo.barry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: tc.input}
			kubeobjects.RemoveAttributes(obj, tc.expr)
			require.Equal(t, tc.expected, obj.Object)
		})
	}
}

func TestRemoveAttributesFromRealManifest(t *testing.T) {
	manifestBytes, err := os.ReadFile(filepath.Join("testdata", "daemonset.json"))
	require.NoError(t, err)
	var original map[string]interface{}
	require.NoError(t, json.Unmarshal(manifestBytes, &original))

	redactedManifestBytes, err := os.ReadFile(filepath.Join("testdata", "daemonset-redacted.json"))
	require.NoError(t, err)
	var expected interface{}
	require.NoError(t, json.Unmarshal(redactedManifestBytes, &expected))

	kubeobjects.RemoveAttributes(&unstructured.Unstructured{Object: original}, "spec.template.spec.containers.env")
	require.Equal(t, expected, original)
}
