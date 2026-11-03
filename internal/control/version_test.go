package control

import (
	"fmt"
	"runtime"
	"testing"
)

func TestVersionUsesApplicationBuildIdentity(t *testing.T) {
	for _, versions := range [][]string{{"release-4350e1b"}, nil, {""}} {
		r := New(nil)
		RegisterBuiltins(r, versions...)
		response, err := r.Dispatch(t.Context(), Command{Name: "version", Caller: Caller{Source: SourceTelegram, ChatID: 1}})
		if err != nil {
			t.Fatal(err)
		}
		got, ok := response.Data["version"].(string)
		if !ok || got == "" {
			t.Fatalf("missing version: %+v", response)
		}
		if len(versions) > 0 && versions[0] != "" && got != versions[0] {
			t.Fatalf("application version ignored: %+v", response)
		}
		if response.Data["go"] != runtime.Version() || response.Text != fmt.Sprintf("gopod %s (%s)", got, runtime.Version()) {
			t.Fatalf("inconsistent text/structured build identity: %+v", response)
		}
	}
}
