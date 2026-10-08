package host

import (
	"context"
	"strings"
	"testing"
	"time"

	apiinstance "github.com/easytier/easytier/easytier-go/proto/api/instance"
	"github.com/easytier/easytier/easytier-go/proto/common"
	"google.golang.org/protobuf/proto"
)

func TestWebManagementCredentialDispatchTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host, err := New(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())

	id := &common.UUID{Part1: 81, Part2: 82, Part3: 83, Part4: 84}
	identifier := &apiinstance.InstanceIdentifier{Selector: &apiinstance.InstanceIdentifier_Id{Id: id}}
	queries := []struct {
		method  string
		request proto.Message
	}{
		{generateCredentialMethod, &apiinstance.GenerateCredentialRequest{Instance: identifier}},
		{upsertCredentialMethod, &apiinstance.UpsertCredentialRequest{Instance: identifier}},
		{revokeCredentialMethod, &apiinstance.RevokeCredentialRequest{Instance: identifier}},
		{listCredentialsMethod, &apiinstance.ListCredentialsRequest{Instance: identifier}},
	}
	for _, query := range queries {
		t.Run(query.method, func(t *testing.T) {
			encoded, err := proto.Marshal(query.request)
			if err != nil {
				t.Fatal(err)
			}
			_, err = host.manager.dispatch(ctx, query.method, encoded, "", nil)
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("dispatch error = %v, want UUID ownership lookup failure", err)
			}
		})
	}
}
