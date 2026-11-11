package foundation

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/discovery"
)

const (
	// VaultReadMaxSnapshots is the maximum number of listing snapshots held by one handler.
	VaultReadMaxSnapshots = 1024
	// VaultReadMaxSnapshotBytes bounds the total message bytes held by one handler's snapshots.
	VaultReadMaxSnapshotBytes = 256 << 20
	// VaultReadSnapshotLifetime is the fixed lifetime of each listing snapshot.
	VaultReadSnapshotLifetime = 15 * time.Minute
	// vaultReadRetryAfterSeconds is the retry delay for exhausted local capacity.
	vaultReadRetryAfterSeconds = 30
)

func init() {
	routeGroups = append(routeGroups, vaultReadRegisterRoutes)
}

func vaultReadRegisterRoutes(s *Service, mux *http.ServeMux) {
	snapshots, err := discovery.NewSnapshots(discovery.SnapshotLimits{
		MaxSnapshots: VaultReadMaxSnapshots,
		MaxBytes:     VaultReadMaxSnapshotBytes,
		Lifetime:     VaultReadSnapshotLifetime,
	}, func() time.Time { return s.Clock() }, nil)
	if err != nil {
		panic(fmt.Sprintf("foundation: invalid vault read snapshot limits: %v", err))
	}
	index := &vaultReadIndex{index: discovery.NewIndex()}
	visibility := vaultReadVisibility{}
	mux.HandleFunc("GET /v1/messages/{message_id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, ok := s.VaultRecord(r.PathValue("message_id"))
		if !ok || !vaultReadDeliverable(record.MessageID) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(record.Message)
	})
	mux.HandleFunc("GET /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		vaultReadList(w, r, s, index, snapshots, visibility, discovery.Messages)
	})
	mux.HandleFunc("GET /v1/author-messages", func(w http.ResponseWriter, r *http.Request) {
		vaultReadList(w, r, s, index, snapshots, visibility, discovery.AuthorMessages)
	})
	mux.HandleFunc("GET /v1/changes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		after, limit, err := discovery.ParseChangesQuery(r.URL.RawQuery)
		if err != nil {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return
		}
		if err := index.sync(s); err != nil {
			vaultReadUnavailable(w)
			return
		}
		now := s.Clock().UTC().Truncate(time.Second)
		providerID, ok := s.Trust().CurrentFoundationID(now)
		if !ok {
			vaultReadUnavailable(w)
			return
		}
		records := index.index.After(after, limit)
		candidates := make([]discovery.Candidate, 0, len(records))
		for _, record := range records {
			candidates = append(candidates, discovery.Candidate{
				Sequence: record.Sequence,
				Bytes:    record.Bytes,
				Visible:  vaultReadDeliverable(record.MessageID),
			})
		}
		complete := len(records) < limit
		body := discovery.BuildChanges(providerID, after, limit, index.index.MaxSequence(), candidates, complete)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /v1/settlements/{receipt_id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, ok := s.VaultRecordByReceiptID(r.PathValue("receipt_id"))
		if !ok || !vaultReadDeliverable(record.MessageID) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
			return
		}
		body, err := canon.Encode(record.Proof)
		if err != nil {
			vaultReadUnavailable(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

func vaultReadList(w http.ResponseWriter, r *http.Request, s *Service, index *vaultReadIndex, snapshots *discovery.Snapshots, visibility discovery.Visibility, route discovery.Route) {
	w.Header().Set("Cache-Control", "no-store")
	query, cursor, err := discovery.ParseListQuery(route, "", r.URL.RawQuery)
	if err != nil {
		var queryErr *discovery.Error
		if errors.As(err, &queryErr) && queryErr.Status == string(apierr.InvalidCursor) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidCursor})
			return
		}
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	if err := index.sync(s); err != nil {
		vaultReadUnavailable(w)
		return
	}
	var body []byte
	if cursor != "" {
		body, err = snapshots.Continue(query.Binding(), cursor, query.Limit, visibility)
	} else {
		body, err = snapshots.Start(query.Binding(), query.Limit, visibility, func() ([]discovery.Entry, error) {
			matches := index.index.Select(query)
			entries := make([]discovery.Entry, 0, len(matches))
			for _, record := range matches {
				entries = append(entries, discovery.Entry{MessageID: record.MessageID, Bytes: record.Bytes})
			}
			return entries, nil
		})
	}
	if err != nil {
		switch {
		case errors.Is(err, discovery.ErrUnknownCursor):
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidCursor})
		case errors.Is(err, discovery.ErrBusy):
			apierr.Write(w, apierr.PublicReadError{Status: apierr.RateLimited, RetryAfterSeconds: vaultReadRetryAfterSeconds})
		default:
			vaultReadUnavailable(w)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func vaultReadUnavailable(w http.ResponseWriter) {
	apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: vaultReadRetryAfterSeconds})
}

// vaultReadDeliverable is consulted by every route; a later legal-restriction issue replaces this
// body here, failing closed when the restriction source is unknown.
func vaultReadDeliverable(_ string) bool { return true }

type vaultReadIndex struct {
	mu                  sync.Mutex
	index               *discovery.Index
	lastIndexedSequence uint64
}

func (i *vaultReadIndex) sync(s *Service) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, stored := range s.VaultRecords() {
		if stored.Sequence <= i.lastIndexedSequence {
			continue
		}
		record, err := discovery.ParseRecord(stored.Message, stored.Sequence)
		if err != nil {
			return err
		}
		i.index.Add(record)
		i.index.ObserveSequence(stored.Sequence)
		i.lastIndexedSequence = stored.Sequence
	}
	return nil
}

type vaultReadVisibility struct{}

func (vaultReadVisibility) Generation() uint64 { return 0 }

func (vaultReadVisibility) Visible(messageID string) (bool, uint64, error) {
	return vaultReadDeliverable(messageID), 0, nil
}
