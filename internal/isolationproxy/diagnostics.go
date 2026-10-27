package isolationproxy

import "errors"

// discardDiagnostics accepts only the native Messages prompt-comparison shape.
// It never forwards the identifier: even optional diagnostics must not expose
// account-scoped prompt fingerprints or become remote-context capabilities.
func discardDiagnostics(value map[string]any, policy BodyPolicy) (bool, error) {
	diagnostics, exists := value["diagnostics"]
	if !exists {
		return false, nil
	}
	if policy != AnthropicMessages {
		return false, errors.New("unapproved diagnostics")
	}
	if diagnostics != nil {
		fields, ok := diagnostics.(map[string]any)
		if !ok {
			return false, errors.New("invalid diagnostics")
		}
		for key, item := range fields {
			if key != "previous_message_id" {
				return false, errors.New("invalid diagnostics")
			}
			if item != nil {
				id, ok := item.(string)
				if !ok || len(id) > 256 {
					return false, errors.New("invalid diagnostics")
				}
			}
		}
	}
	delete(value, "diagnostics")
	return true, nil
}
