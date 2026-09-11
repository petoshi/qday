package localapp

import "testing"

func TestEndpointRejectsCredentialExfiltration(t *testing.T) {
	for _, s := range []string{"http://evil.example:80", "http://127.0.0.1.evil.example:80", "http://localhost:9988", "https://127.0.0.1:9988", "http://user@127.0.0.1:9988", "http://127.0.0.1:9988/path", "http://127.0.0.1:9988?redirect=1", "http://127.0.0.1:0"} {
		if (Endpoint{Format: 1, URL: s}).Validate() == nil {
			t.Fatalf("accepted unsafe endpoint %q", s)
		}
	}
	e := Endpoint{Format: 1, URL: "http://127.0.0.1:41234", PID: 123}
	dir := t.TempDir()
	if err := WriteEndpoint(dir, e); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEndpoint(dir)
	if err != nil || got != e {
		t.Fatal("endpoint discovery failed")
	}
}
