package rootbroker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This contract makes additions to the privileged RPC envelope explicit.
// In particular, an untrusted panel caller must never choose a helper,
// executable, argv, or generic command. Add a typed operation and validator
// contract when expanding the broker's capability set.
func TestBrokerRPCSurfaceRemainsTyped(t *testing.T) {
	typeOfRequest := reflect.TypeOf(Request{})
	wantFields := []string{
		"RequestType", "Fencing", "Site", "App", "Worker", "Runner", "DB",
		"Vhost", "Proxy", "Git", "Account", "Files", "Certificate", "Task",
		"Environment", "Resource", "WordPress",
	}
	if typeOfRequest.NumField() != len(wantFields) {
		t.Fatalf("broker Request has %d fields, want the reviewed %d-field typed envelope; inspect new RPC capabilities explicitly", typeOfRequest.NumField(), len(wantFields))
	}
	for i, want := range wantFields {
		if got := typeOfRequest.Field(i).Name; got != want {
			t.Fatalf("broker Request field %d = %q, want %q; inspect the RPC surface change", i, got, want)
		}
	}

	forbiddenJSONFields := map[string]bool{
		"helper": true, "helper_path": true, "helper_args": true,
		"executable": true, "executable_path": true, "program": true,
		"argv": true, "args": true, "command_path": true,
	}
	for i := 0; i < typeOfRequest.NumField(); i++ {
		field := typeOfRequest.Field(i)
		if field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		payloadType := field.Type.Elem()
		for j := 0; j < payloadType.NumField(); j++ {
			payloadField := payloadType.Field(j)
			jsonName := strings.Split(payloadField.Tag.Get("json"), ",")[0]
			if forbiddenJSONFields[jsonName] {
				t.Errorf("typed payload %s exposes forbidden generic execution field %q", payloadType.Name(), jsonName)
			}
			// TaskRequest.Command describes the customer's scheduled workload;
			// it is not an executable or helper selector. Keep that distinct
			// intent from becoming a generic broker command field elsewhere.
			if strings.EqualFold(jsonName, "command") && (payloadType != reflect.TypeOf(TaskRequest{}) || jsonName != "command") {
				t.Errorf("generic command field %s.%s is outside the task workload contract", payloadType.Name(), payloadField.Name)
			}
		}
	}
}

func TestBrokerRejectsGenericHelperWireRequest(t *testing.T) {
	var request Request
	if err := json.Unmarshal([]byte(`{"type":"helper","helper":{"executable":"/bin/sh","args":["-c","id"]}}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := NewValidator("/var/www").ValidateRequest(&request); err == nil {
		t.Fatal("generic helper/executable RPC request was accepted")
	}
}
