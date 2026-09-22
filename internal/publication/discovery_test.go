package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestListForAnalyticsPaginatesReservedRowsWithoutMutatingState(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	o, db := newTestOrchestrator(t, now, &fakeAdapter{platform: PlatformDouyin, configured: true})
	for i := 0; i < 205; i++ {
		id := fmt.Sprintf("pub-%03d", i)
		status := StatusSubmitted
		operationToken := ""
		leaseUntil := int64(0)
		if i == 204 {
			status = StatusCommitting
			operationToken = "expired-operation"
			leaseUntil = now.Add(-time.Hour).UnixNano()
		}
		seedAnalyticsPublication(t, db, now, id, status, formatTime(now), operationToken, leaseUntil)
	}
	// These rows exercise both discovery filters independently.
	seedAnalyticsPublication(t, db, now, "pub-draft", StatusDraftReady, formatTime(now), "", 0)
	seedAnalyticsPublication(t, db, now, "pub-unreserved", StatusSubmitted, "", "", 0)

	var beforeStatus, beforeOperation, beforeUpdated string
	var beforeLease int64
	if err := db.QueryRowContext(context.Background(), `SELECT status,operation_token,lease_until,updated_at FROM pub_publications WHERE id=?`, "pub-204").Scan(&beforeStatus, &beforeOperation, &beforeLease, &beforeUpdated); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	first, err := o.ListForAnalytics(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 200 || first[0].ID != "pub-000" || first[len(first)-1].ID != "pub-199" {
		t.Fatalf("first page = len %d, first %q, last %q", len(first), first[0].ID, first[len(first)-1].ID)
	}

	second, err := o.ListForAnalytics(ctx, first[len(first)-1].ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 5 || second[0].ID != "pub-200" || second[len(second)-1].ID != "pub-204" {
		t.Fatalf("second page = len %d, first %q, last %q", len(second), second[0].ID, second[len(second)-1].ID)
	}
	for _, item := range append(first, second...) {
		if item.ID == "pub-draft" || item.ID == "pub-unreserved" || item.ReservedAt.IsZero() {
			t.Fatalf("analytics returned an ineligible publication: %+v", item)
		}
	}
	third, err := o.ListForAnalytics(ctx, second[len(second)-1].ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 0 {
		t.Fatalf("third page = %d items, want empty", len(third))
	}

	var afterStatus, afterOperation, afterUpdated string
	var afterLease int64
	if err := db.QueryRowContext(ctx, `SELECT status,operation_token,lease_until,updated_at FROM pub_publications WHERE id=?`, "pub-204").Scan(&afterStatus, &afterOperation, &afterLease, &afterUpdated); err != nil {
		t.Fatal(err)
	}
	if beforeStatus != afterStatus || beforeOperation != afterOperation || beforeLease != afterLease || beforeUpdated != afterUpdated {
		t.Fatalf("analytics read mutated expired reservation: before=(%q,%q,%d,%q) after=(%q,%q,%d,%q)", beforeStatus, beforeOperation, beforeLease, beforeUpdated, afterStatus, afterOperation, afterLease, afterUpdated)
	}
}

func seedAnalyticsPublication(t *testing.T, db *sql.DB, now time.Time, id string, status Status, reservedAt, operationToken string, leaseUntil int64) {
	t.Helper()
	pkg := packageFor(t, "analytics-account", PlatformDouyin, id)
	pkg.VariantID = id
	pkg.Copy = "analytics copy " + id
	pkg.CopySHA256 = ""
	fingerprint, err := pkg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	externalID := "external-" + id
	evidence := fmt.Sprintf(`{"status":"submitted","external_id":%q,"verified":true}`, externalID)
	_, err = db.ExecContext(context.Background(), `INSERT INTO pub_publications(
		id,creator,run_id,account,platform,variant_id,package_json,fingerprint,policy_version,business_day,authorization_id,status,
		reservation_token,operation_token,lease_until,external_id,external_url,evidence,last_error,reserved_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, pkg.Creator, pkg.Run, pkg.Account, pkg.Platform, pkg.VariantID, string(raw), fingerprint, "v1", "2026-09-21", "auth-"+id, status,
		"reservation-"+id, operationToken, leaseUntil, externalID, "https://example.test/"+id, evidence, "", reservedAt, formatTime(now), formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
}
