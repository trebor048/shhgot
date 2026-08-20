package aireview

import (
	"testing"
	"time"
)

func TestNotifyStore(t *testing.T) {
	ns, err := NewNotifyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := ns.Append(Notification{ID: "n1", CreatedAt: now, Kind: NotifyDud, JobID: "j1", Message: "dud"}); err != nil {
		t.Fatal(err)
	}
	if err := ns.Append(Notification{ID: "n2", CreatedAt: now.Add(time.Second), Kind: NotifyCompleted, JobID: "j2", Message: "done"}); err != nil {
		t.Fatal(err)
	}
	list, err := ns.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "n2" {
		t.Fatalf("list wrong: %+v", list)
	}
	if n, _ := ns.UnreadCount(); n != 2 {
		t.Fatalf("unread = %d, want 2", n)
	}
	if err := ns.MarkAllRead(); err != nil {
		t.Fatal(err)
	}
	if n, _ := ns.UnreadCount(); n != 0 {
		t.Fatalf("unread after mark-read = %d, want 0", n)
	}
}
