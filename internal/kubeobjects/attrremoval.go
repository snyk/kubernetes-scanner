package kubeobjects

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// `expr` is a dot-separated address for nested values, in the same format as
// arguments to `kubectl explain`.
// For example, the expr "spec.containers.env" will cause Kubernetes Pod
// container environment variables to be removed. "containers" is an array, and
// each element of this array is redacted.
func RemoveAttributes(obj *unstructured.Unstructured, expr string) {
	exprParts := strings.Split(expr, ".")
	// we always pass in a map[string]interface{}, so we should always get the same type out.
	obj.Object = removeAttributes(obj.Object, exprParts).(map[string]interface{})
}

// removeAttributes removes the dot-separated address from the given JSON object. "JSON object"
// means it needs to be a JSON compatible type, which is either a basic type (string, float, int or
// bool), []interface{} or map[string]interface{}.
// These are the same constraints that an `unstructured.Unstructured.Object` has
// (see https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured#Unstructured).
func removeAttributes(jsonObj interface{}, exprParts []string) interface{} {
	currentPart := exprParts[0]
	remainingParts := exprParts[1:]

	switch val := jsonObj.(type) {
	case map[string]interface{}:
		if len(remainingParts) == 0 {
			delete(val, currentPart)
			return val
		}

		val[currentPart] = removeAttributes(val[currentPart], remainingParts)
		return val

	case []interface{}:
		redactedVals := make([]interface{}, len(val))
		for i, v := range val {
			redactedVals[i] = removeAttributes(v, exprParts)
		}
		return redactedVals

	default:
		// Since the root node of a Kubernetes resource is always a map, and
		// removal expressions contain key names, we should be guaranteed to
		// remove desired scalar values when the function is visiting a map. We only
		// land in this branch when we are redacting a mixed-type array that
		// contains some scalars. We can return the scalars directly here.
		return val
	}
}
