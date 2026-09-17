package chain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"go.sia.tech/core/types"
)

func TestQdayUpgradeScheduleDoesNotChangeGenesis(t *testing.T) {
	owner, err := types.ParseQdayAddress("qday1pdsa0ezy7y3nnmnxm0tx74q2kd9acvzs8329wfd2n6ycqnmygtt9stpwtsr")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewQdayManifestWithMessage(owner, time.Date(2026, 9, 11, 6, 59, 0, 0, time.UTC), false, "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME.")
	if err != nil {
		t.Fatal(err)
	}
	wantGenesis, wantDomain := manifest.Genesis.ID(), manifest.Network.Qday.Domain
	original, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Network.Qday.V1Height = 987654321
	changed, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, changed) {
		t.Fatal("post-launch activation schedule leaked into the immutable manifest")
	} else if manifest.Genesis.ID() != wantGenesis || manifest.Network.Qday.Domain != wantDomain {
		t.Fatal("post-launch activation schedule changed the genesis identity")
	}

	var loaded QdayManifest
	if err := json.Unmarshal(original, &loaded); err != nil {
		t.Fatal(err)
	} else if loaded.Network.Qday.V1Height != 0 {
		t.Fatal("serialized manifest unexpectedly contains the activation schedule")
	} else if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	} else if loaded.Network.Qday.V1Height != QdayV1ActivationHeight {
		t.Fatalf("loaded schedule is %d, want %d", loaded.Network.Qday.V1Height, QdayV1ActivationHeight)
	}
}
