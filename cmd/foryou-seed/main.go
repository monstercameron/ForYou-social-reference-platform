package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
	"github.com/joho/godotenv"
)

type options struct {
	provider, foundation, monitor, faux, mediaBase, mediaDir, stateDir string
	timeout                                                            time.Duration
}

type seedManifest struct {
	ProviderID string            `json:"provider_id"`
	Messages   map[string]string `json:"messages"`
}

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "load .env:", err)
		os.Exit(1)
	}
	if os.Getenv("FORYOU_DEV") != "1" {
		fmt.Fprintln(os.Stderr, "foryou-seed is dev only; set FORYOU_DEV=1")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, parseOptions()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseOptions() options {
	var o options
	flag.StringVar(&o.provider, "provider", roleURL("PROVIDER", "http://127.0.0.1:8445"), "provider URL")
	flag.StringVar(&o.foundation, "foundation", roleURL("FOUNDATION", "http://127.0.0.1:8444"), "Foundation URL")
	flag.StringVar(&o.monitor, "monitor", roleURL("MONITOR", "http://127.0.0.1:8447"), "monitor URL")
	flag.StringVar(&o.faux, "faux", roleURL("FAUX", "http://127.0.0.1:8448"), "faux-chain URL")
	flag.StringVar(&o.mediaBase, "media-base", "https://monstercameron.github.io/ForYou-social-reference-platform/demo-media", "public media URL base")
	flag.StringVar(&o.mediaDir, "media-dir", "docs/demo-media", "directory containing exact demo media bytes")
	flag.StringVar(&o.stateDir, "state-dir", ".local/seed", "durable seed state directory")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Minute, "settlement timeout")
	flag.Parse()
	return o
}

func roleURL(role, fallback string) string {
	if value := os.Getenv("FORYOU_" + role + "_URL"); value != "" {
		return value
	}
	return fallback
}

func run(parent context.Context, o options) error {
	if o.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	urls := map[string]string{"faux": o.faux, "foundation": o.foundation, "monitor": o.monitor, "provider": o.provider}
	for _, role := range []string{"faux", "foundation", "provider", "monitor"} {
		if err := checkLoopbackURL(urls[role]); err != nil {
			return fmt.Errorf("%s URL: %w", role, err)
		}
		if err := health(urls[role]); err != nil {
			return fmt.Errorf("%s is unavailable at %s; start it with FORYOU_DEV=1 go run ./cmd/foryou -role %s", role, urls[role], role)
		}
	}
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	foundationID := keys.DIDOf(devnet.FoundationKey())
	provider, err := sdk.New(o.provider, sdk.Options{ServiceID: providerID, AllowLoopbackHTTP: true})
	if err != nil {
		return err
	}
	foundation, err := sdk.New(o.foundation, sdk.Options{ServiceID: foundationID, AllowLoopbackHTTP: true})
	if err != nil {
		return err
	}
	genesis, genesisRaw, err := foundation.Genesis(parent)
	if err != nil {
		return roleError("foundation", o.foundation, err)
	}
	certs, certRaw, err := foundation.Certificates(parent)
	if err != nil {
		return roleError("foundation", o.foundation, err)
	}
	schedules, scheduleRaw, err := foundation.FeeSchedules(parent)
	if err != nil {
		return roleError("foundation", o.foundation, err)
	}
	distribution, distributionRaw, err := foundation.Distribution(parent)
	if err != nil {
		return roleError("foundation", o.foundation, err)
	}
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: devnet.Genesis().ReleaseKeyIDs, Threshold: int(devnet.Genesis().ReleaseThreshold)}, sdk.Publications{Genesis: genesis, GenesisRaw: genesisRaw, Certificates: certs, CertificatesRaw: certRaw, FeeSchedules: schedules, FeeSchedulesRaw: scheduleRaw, Distribution: distribution, DistributionRaw: distributionRaw, Endpoint: o.foundation, FetchedAt: time.Now()})
	if err != nil {
		return fmt.Errorf("Foundation trust publications: %w", err)
	}
	journal, err := sdk.FileJournal(filepath.Join(o.stateDir, "journal"))
	if err != nil {
		return err
	}
	pub := sdk.NewPublisher(provider, root, journal, time.Now)
	verifier := sdk.NewVerifier(sdk.VerifierConfig{Root: root, Trusts: func(id string) bool { return id == providerID }})
	verifiedMessages := make(map[string]wire.PostMessage)
	reuseChecked := false
	fa := faux.NewClient(o.faux, nil)
	media := make(map[string][]byte)
	for _, file := range []string{"landscape-1.png", "landscape-2.png", "landscape-3.png", "flower.mp4", "avatar-1.png", "avatar-2.png", "avatar-3.png"} {
		data, readErr := os.ReadFile(filepath.Join(o.mediaDir, file))
		if readErr != nil {
			return fmt.Errorf("read media %s: %w", file, readErr)
		}
		media[file] = data
	}
	if err := validateMediaBase(o.mediaBase); err != nil {
		return err
	}
	authors := allAuthors()
	for _, author := range authors {
		fmt.Printf("%s %s\n", author.label, keys.DIDOf(author.key))
	}
	if err := fundAll(parent, provider, fa, o, root, authors); err != nil {
		return err
	}
	manifestPath := filepath.Join(o.stateDir, "manifest.json")
	manifest, err := loadManifest(manifestPath, providerID)
	if err != nil {
		return err
	}
	plan := showcasePlan(media, o.mediaBase)
	ids := manifest.Messages
	if ids == nil {
		ids = make(map[string]string)
	}
	nameResults := make(map[string]string)
	for _, item := range plan[:8] {
		item = resolvePlanIDs(item, ids)
		id, publishErr := publishOne(parent, provider, pub, journal, authors, verifier, verifiedMessages, &reuseChecked, item, ids, manifestPath, providerID)
		if publishErr != nil {
			return publishErr
		}
		ids[item.key] = id
		if err := saveManifest(manifestPath, seedManifest{ProviderID: providerID, Messages: ids}); err != nil {
			return err
		}
	}
	demo := demoPlan(media, o.mediaBase)
	for _, item := range demo {
		if item.parentKey != "" {
			item.draft.replyTo = ids[item.parentKey]
		}
		id, publishErr := publishOne(parent, provider, pub, journal, authors, verifier, verifiedMessages, &reuseChecked, item, ids, manifestPath, providerID)
		if publishErr != nil {
			return publishErr
		}
		ids[item.key] = id
		if item.name != nil {
			result, nameErr := publishName(parent, provider, item, authors, media, o.mediaBase)
			if nameErr != nil {
				return nameErr
			}
			nameResults[item.author] = result
		}
		if err := saveManifest(manifestPath, seedManifest{ProviderID: providerID, Messages: ids}); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parent, o.timeout)
	defer cancel()
	waitIDs, receiptEntries, err := unsettledReceipts(journal, append(append([]plannedPost(nil), plan[:8]...), demo...), ids)
	if err != nil {
		return err
	}
	if err := waitForReceipts(ctx, waitIDs, 2*time.Second, func(ctx context.Context, receiptID string) (bool, error) {
		proof, proofErr := retryLimited(ctx, func() (wire.SettlementProof, error) {
			proof, _, err := provider.Settlement(ctx, receiptID)
			return proof, err
		})
		if proofErr != nil {
			if sdk.Classify(proofErr) == sdk.SignalNotFound {
				return false, errNotFound
			}
			return false, proofErr
		}
		entry, found := receiptEntries[receiptID]
		if !found {
			return false, fmt.Errorf("missing journal receipt %s", receiptID)
		}
		msg := verifier.Verify(entry.FinalMessage, entry.MessageID, sdk.Source{Endpoint: provider.BaseURL(), ServiceID: providerID, Kind: sdk.SourceProvider})
		msg = verifier.Settle(msg, &proof, nil)
		return msg.Settlement == settlement.StateSettled, nil
	}); err != nil {
		return err
	}
	for _, item := range plan[8:] {
		item = resolvePlanIDs(item, ids)
		id, e := publishOne(parent, provider, pub, journal, authors, verifier, verifiedMessages, &reuseChecked, item, ids, manifestPath, providerID)
		if e != nil {
			return e
		}
		ids[item.key] = id
	}
	if err := saveManifest(manifestPath, seedManifest{ProviderID: providerID, Messages: ids}); err != nil {
		return err
	}
	allPlan := append(append([]plannedPost(nil), plan...), demo...)
	return printSummary(parent, provider, journal, verifier, allPlan, ids, authors, nameResults, o.mediaBase)
}

func checkLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid URL")
	}
	ip := net.ParseIP(u.Hostname())
	if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("host must be loopback")
	}
	return nil
}
func health(base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(strings.TrimRight(base, "/") + "/healthz")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", res.StatusCode)
	}
	return nil
}
func roleError(role, base string, err error) error {
	return fmt.Errorf("%s at %s: %v", role, base, err)
}

func loadManifest(path, providerID string) (seedManifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return seedManifest{ProviderID: providerID, Messages: map[string]string{}}, nil
	}
	if err != nil {
		return seedManifest{}, err
	}
	var m seedManifest
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.ProviderID != providerID {
		return m, errors.New("seed state belongs to another provider; delete it or pass another -state-dir")
	}
	return m, nil
}
func saveManifest(path string, m seedManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func fundAll(ctx context.Context, provider *sdk.Client, chain *faux.Client, o options, root *sdk.TrustRoot, authors []seedAuthor) error {
	for _, a := range authors {
		networks := []string{wire.BitcoinMainnet}
		if a.label == "seed-author-3" {
			networks = append(networks, wire.EthereumMainnet)
		}
		for _, network := range networks {
			log, err := sdk.FileFundingLog(filepath.Join(o.stateDir, a.label, network))
			if err != nil {
				return err
			}
			attempts, err := log.Attempts()
			if err != nil {
				return err
			}
			for _, attempt := range attempts {
				if sdk.NeedsReconciliation(attempt) {
					if err := reconcileFunding(ctx, provider, chain, a, network, attempt, log); err != nil {
						return err
					}
				}
			}
			balance, err := retryLimited(ctx, func() (wire.BalanceStatus, error) { return provider.Balance(ctx, a.key, network) })
			if err != nil {
				return roleError("provider", o.provider, err)
			}
			confirmed := new(big.Int)
			for _, amount := range []string{balance.Available, balance.Frozen, balance.Reserved} {
				if n, ok := new(big.Int).SetString(amount, 10); ok {
					confirmed.Add(confirmed, n)
				}
			}
			if confirmed.Cmp(big.NewInt(1000000)) >= 0 {
				continue
			}
			schedule, ok := root.ScheduleInForce(network, time.Now())
			if !ok {
				return fmt.Errorf("no fee schedule for %s", network)
			}
			amount := "10000"
			if network == wire.EthereumMainnet {
				amount = "1000000000000000"
			}
			invoice, err := retryLimited(ctx, func() (wire.DepositInvoice, error) {
				inv, _, requestErr := provider.RequestDeposit(ctx, a.key, sdk.DepositPlan{Network: network, AmountChainUnits: amount, DisclosureAcknowledged: true, Schedule: schedule, Confirmed: confirmed.String(), Pending: "0"}, log)
				return inv, requestErr
			})
			if err != nil {
				return err
			}
			transfer, err := chain.Submit(ctx, faux.SubmitRequest{Network: network, From: faux.DevAddress(network, a.label), Recipient: invoice.Recipient, AmountChainUnits: amount})
			if err != nil {
				return roleError("faux", o.faux, err)
			}
			need := faux.BitcoinConfirmations - 1
			if network == wire.EthereumMainnet {
				need = faux.EthereumFinalityDepth - 1
			}
			if _, err = chain.Mine(ctx, network, need); err != nil {
				return err
			}
			var output *int64
			if transfer.OutputIndex != nil {
				n := int64(*transfer.OutputIndex)
				output = &n
			}
			hint := wire.DepositTransfer{Network: network, TxID: transfer.TxID, OutputIndex: output, TxHash: transfer.TxHash}
			for {
				result, observeErr := retryLimited(ctx, func() (sdk.ObserveResult, error) {
					return provider.ObserveDeposit(ctx, a.key, invoice, hint, log)
				})
				if observeErr != nil {
					return observeErr
				}
				if !result.Pending {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
		}
	}
	return nil
}

func reconcileFunding(ctx context.Context, provider *sdk.Client, chain *faux.Client, author seedAuthor, network string, attempt sdk.FundingAttempt, log sdk.FundingLog) error {
	if attempt.Invoice == nil || attempt.Transfer == nil {
		return fmt.Errorf("funding attempt %s cannot be reconciled from its log", attempt.RequestID)
	}
	lookup := attempt.Transfer.TxID
	if lookup == "" {
		lookup = attempt.Transfer.TxHash
	}
	transfer, err := chain.Transfer(ctx, network, lookup)
	if err != nil {
		return err
	}
	depth := faux.BitcoinConfirmations
	if network == wire.EthereumMainnet {
		depth = faux.EthereumFinalityDepth
	}
	if !transfer.Finalized && int(transfer.Confirmations) < depth {
		if _, err := chain.Mine(ctx, network, depth-int(transfer.Confirmations)); err != nil {
			return err
		}
	}
	result, err := retryLimited(ctx, func() (sdk.ObserveResult, error) {
		return provider.ObserveDeposit(ctx, author.key, *attempt.Invoice, *attempt.Transfer, log)
	})
	if err != nil {
		return err
	}
	if result.Pending {
		return fmt.Errorf("funding attempt %s remains pending after finality", attempt.RequestID)
	}
	return nil
}

func retryLimited[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for {
		value, err := call()
		var serviceErr *sdk.Error
		if !errors.As(err, &serviceErr) || serviceErr.HTTPStatus != http.StatusTooManyRequests {
			return value, err
		}
		delay := serviceErr.RetryAfter
		if delay < time.Second {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, ctx.Err()
		case <-timer.C:
		}
	}
}

func publishName(ctx context.Context, provider *sdk.Client, item plannedPost, authors []seedAuthor, media map[string][]byte, base string) (string, error) {
	var author *seedAuthor
	for i := range authors {
		if authors[i].label == item.author {
			author = &authors[i]
			break
		}
	}
	if author == nil || item.name == nil {
		return "", errors.New("missing signer for planned name")
	}
	authorID := keys.DIDOf(author.key)
	current, err := retryLimited(ctx, func() (names.NameRecord, error) { return provider.Name(ctx, authorID) })
	var previous *string
	if err == nil {
		if current.DisplayName != nil && *current.DisplayName == item.name.displayName && sameAvatar(current.Avatar, item.name.avatar, media, base) {
			return "already held", nil
		}
		previous = &current.NameID
	} else if sdk.Classify(err) != sdk.SignalNotFound {
		return "", err
	}
	name := item.name.displayName
	var avatar *names.Avatar
	if item.name.avatar != "" {
		asset := media[item.name.avatar]
		avatarURL := strings.TrimRight(base, "/") + "/" + item.name.avatar
		if _, err := content.ParseExternalURL(avatarURL); err != nil {
			return "", err
		}
		avatar = &names.Avatar{URL: avatarURL, SHA256: digest(asset), SizeBytes: int64(len(asset))}
	}
	record, err := names.Build(author.key, &name, avatar, time.Now().UTC().Truncate(time.Second), previous)
	if err != nil {
		return "", err
	}
	result, err := retryLimited(ctx, func() (sdk.NamePublishResult, error) { return provider.PublishName(ctx, record) })
	if err != nil {
		return "", err
	}
	if result == sdk.NameAlreadyHeld {
		return "already held", nil
	}
	return "published", nil
}

func sameAvatar(current *names.Avatar, filename string, media map[string][]byte, base string) bool {
	if filename == "" {
		return current == nil
	}
	if current == nil {
		return false
	}
	return current.URL == strings.TrimRight(base, "/")+"/"+filename && current.SHA256 == digest(media[filename]) && current.SizeBytes == int64(len(media[filename]))
}

type authorSummary struct {
	posts, replies int
	fees           map[string]*big.Int
	states         map[string]int
}

func printSummary(ctx context.Context, provider *sdk.Client, journal sdk.Journal, verifier *sdk.Verifier, plan []plannedPost, ids map[string]string, authors []seedAuthor, nameResults map[string]string, mediaBase string) error {
	states := make(map[string]settlement.State, len(plan))
	entries := make(map[string]sdk.JournalEntry, len(plan))
	byAuthor := make(map[string]*authorSummary, len(authors))
	totals := make(map[string]*big.Int)
	totalStates := make(map[string]int)
	for _, author := range authors {
		byAuthor[author.label] = &authorSummary{fees: make(map[string]*big.Int), states: make(map[string]int)}
	}
	for _, item := range plan {
		id := ids[item.key]
		entry, ok, err := journal.Get(id)
		if err != nil || !ok {
			return fmt.Errorf("missing journal entry for %s", item.key)
		}
		record := verifier.Verify(entry.FinalMessage, id, sdk.Source{Endpoint: provider.BaseURL(), ServiceID: provider.ServiceID(), Kind: sdk.SourceProvider})
		if record.Class == sdk.ClassInvalid {
			return fmt.Errorf("message %s failed SDK verification at summary", id)
		}
		proof, proofErr := retryLimited(ctx, func() (wire.SettlementProof, error) {
			proof, _, err := provider.Settlement(ctx, entry.Receipt.ReceiptID)
			return proof, err
		})
		var proofPtr *wire.SettlementProof
		if proofErr == nil {
			proofPtr = &proof
		} else if sdk.Classify(proofErr) != sdk.SignalNotFound {
			return roleError("provider", provider.BaseURL(), proofErr)
		}
		record = verifier.Settle(record, proofPtr, nil)
		states[item.key], entries[item.key] = record.Settlement, entry
		summary := byAuthor[item.author]
		if summary == nil {
			return fmt.Errorf("unknown summary author %s", item.author)
		}
		if item.kind == "reply" {
			summary.replies++
		} else {
			summary.posts++
		}
		summary.states[string(record.Settlement)]++
		totalStates[string(record.Settlement)]++
		amount, ok := new(big.Int).SetString(entry.Quote.AmountBaseUnits, 10)
		if !ok {
			return fmt.Errorf("invalid quoted fee on %s", item.key)
		}
		if summary.fees[entry.Quote.Network] == nil {
			summary.fees[entry.Quote.Network] = new(big.Int)
		}
		summary.fees[entry.Quote.Network].Add(summary.fees[entry.Quote.Network], amount)
		if totals[entry.Quote.Network] == nil {
			totals[entry.Quote.Network] = new(big.Int)
		}
		totals[entry.Quote.Network].Add(totals[entry.Quote.Network], amount)
	}
	fmt.Println("author\tkind\tmessage_id\tnetwork\tfee\tstate")
	for _, item := range plan[:12] {
		entry := entries[item.key]
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", item.author, item.kind, ids[item.key], entry.Quote.Network, entry.Quote.AmountBaseUnits, states[item.key])
	}
	fmt.Println("author\tdid:key\tposts\treplies\ttotal fees\tsettled/pending_settlement/unsettled")
	for _, author := range authors {
		stats := byAuthor[author.label]
		fmt.Printf("%s\t%s\t%d\t%d\t%s\t%d/%d/%d\n", author.label, keys.DIDOf(author.key), stats.posts, stats.replies, formatFees(stats.fees), stats.states[string(settlement.StateSettled)], stats.states[string(settlement.StatePending)], stats.states[string(settlement.StateUnsettled)])
	}
	fmt.Printf("Totals: posts=%d replies=%d fees=%s states=%d/%d/%d\n", countDemoPosts(plan), countDemoReplies(plan), formatFees(totals), totalStates[string(settlement.StateSettled)], totalStates[string(settlement.StatePending)], totalStates[string(settlement.StateUnsettled)])
	fmt.Printf("Names: %s\n", formatNameResults(nameResults))
	fmt.Println("Batch B settles within a few claim periods.")
	if isPrivateMediaBase(mediaBase) {
		fmt.Printf("Blocked media without an operator allowlist: %s\n", strings.Join(blockedMediaKeys(plan), ", "))
	}
	return nil
}

func formatFees(fees map[string]*big.Int) string {
	parts := make([]string, 0, len(fees))
	if btc := fees[wire.BitcoinMainnet]; btc != nil {
		parts = append(parts, btc.String()+" msat")
	}
	if eth := fees[wire.EthereumMainnet]; eth != nil {
		parts = append(parts, eth.String()+" wei")
	}
	return strings.Join(parts, " + ")
}

func formatNameResults(results map[string]string) string {
	parts := make([]string, 0, len(results))
	for _, author := range []string{"seed-signer-1", "seed-signer-2", "seed-signer-3", "seed-signer-4", "seed-signer-5", "seed-signer-6"} {
		if result := results[author]; result != "" {
			parts = append(parts, author+"="+result)
		}
	}
	return strings.Join(parts, ", ")
}

func isPrivateMediaBase(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := strings.Trim(u.Hostname(), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func blockedMediaKeys(plan []plannedPost) []string {
	keys := []string{}
	for _, item := range plan {
		if len(item.draft.media) > 0 {
			keys = append(keys, item.key)
		}
		if item.name != nil && item.name.avatar != "" {
			keys = append(keys, "name:"+item.author)
		}
	}
	return keys
}
func publishOne(ctx context.Context, client *sdk.Client, publisher *sdk.Publisher, journal sdk.Journal, authors []seedAuthor, verifier *sdk.Verifier, verified map[string]wire.PostMessage, reuseChecked *bool, item plannedPost, ids map[string]string, manifestPath, providerID string) (string, error) {
	var author *seedAuthor
	for i := range authors {
		if authors[i].label == item.author {
			author = &authors[i]
			break
		}
	}
	if author == nil {
		return "", fmt.Errorf("unknown author %s", item.author)
	}
	if existing := ids[item.key]; existing != "" {
		entry, ok, err := journal.Get(existing)
		if err != nil {
			return "", err
		}
		if ok && entry.State == "accepted" {
			if !*reuseChecked {
				if _, readErr := retryLimited(ctx, func() (sdk.Fetched, error) { return client.GetMessage(ctx, existing) }); readErr != nil {
					return "", fmt.Errorf("seed state belongs to an earlier devnet: provider does not have %s; delete the state directory or pass another -state-dir", existing)
				}
				*reuseChecked = true
			}
			message, verifyErr := verifyFinal(verifier, entry.FinalMessage, existing, client)
			if verifyErr != nil {
				return "", verifyErr
			}
			verified[existing] = message
			return existing, nil
		}
		if ok && (entry.State == "authorized" || entry.State == "admitted" || entry.State == "signed") {
			entry, err = publisher.Resume(ctx, author.key, existing)
			if err != nil {
				return "", err
			}
			message, verifyErr := verifyFinal(verifier, entry.FinalMessage, existing, client)
			if verifyErr != nil {
				return "", verifyErr
			}
			verified[existing] = message
			return existing, nil
		}
	}
	d := sdk.Draft{ContentFormat: item.draft.format, Text: item.draft.text, MediaSlots: item.draft.media, Refs: item.draft.refs, ReplyTo: item.draft.replyTo, Supersedes: item.draft.supersedes}
	var target *sdk.TargetEvidence
	if d.Supersedes != "" {
		message, exists := verified[d.Supersedes]
		if !exists {
			entry, ok, getErr := journal.Get(d.Supersedes)
			if getErr != nil || !ok {
				return "", fmt.Errorf("missing verified update target %s", d.Supersedes)
			}
			var verifyErr error
			if message, verifyErr = verifyFinal(verifier, entry.FinalMessage, d.Supersedes, client); verifyErr != nil {
				return "", verifyErr
			}
			verified[d.Supersedes] = message
		}
		target = &sdk.TargetEvidence{Message: message}
	}
	quoted, err := publisher.QuoteDraft(ctx, d, author.key, item.network, target)
	if err != nil {
		return "", err
	}
	ids[item.key] = quoted.Prepared.MessageID
	if err := saveManifest(manifestPath, seedManifest{ProviderID: providerID, Messages: ids}); err != nil {
		return "", err
	}
	approval := sdk.Approval{MessageID: quoted.Prepared.MessageID, Amount: quoted.Quote.AmountBaseUnits, Network: quoted.Quote.Network, ApprovedAt: time.Now()}
	published, err := publisher.Publish(ctx, author.key, quoted, approval)
	if err != nil {
		if !sdk.IsAmbiguous(err) {
			return "", err
		}
		entry, resumeErr := publisher.Resume(ctx, author.key, quoted.Prepared.MessageID)
		if resumeErr != nil {
			return "", errors.Join(err, resumeErr)
		}
		if entry.State != "accepted" {
			return "", err
		}
		published = sdk.Published{MessageID: entry.MessageID, Raw: entry.FinalMessage, Retry: true}
	}
	fetched, err := retryLimited(ctx, func() (sdk.Fetched, error) { return client.GetMessage(ctx, published.MessageID) })
	if err != nil {
		return "", err
	}
	verifiedRecord := verifier.Verify(fetched.Raw, published.MessageID, sdk.Source{Endpoint: client.BaseURL(), ServiceID: client.ServiceID(), Kind: sdk.SourceProvider})
	if verifiedRecord.Class == sdk.ClassInvalid {
		return "", fmt.Errorf("provider returned an invalid message %s", published.MessageID)
	}
	verified[published.MessageID] = verifiedRecord.Message
	return published.MessageID, nil
}

func verifyFinal(verifier *sdk.Verifier, raw []byte, id string, client *sdk.Client) (wire.PostMessage, error) {
	record := verifier.Verify(raw, id, sdk.Source{Endpoint: client.BaseURL(), ServiceID: client.ServiceID(), Kind: sdk.SourceProvider})
	if record.Class == sdk.ClassInvalid {
		return wire.PostMessage{}, fmt.Errorf("stored message %s failed SDK verification", id)
	}
	return record.Message, nil
}

func unsettledReceipts(j sdk.Journal, plan []plannedPost, ids map[string]string) ([]string, map[string]sdk.JournalEntry, error) {
	result := []string{}
	entries := make(map[string]sdk.JournalEntry, len(plan))
	for _, item := range plan {
		id := ids[item.key]
		if id == "" {
			continue
		}
		e, ok, err := j.Get(id)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			result = append(result, e.Receipt.ReceiptID)
			entries[e.Receipt.ReceiptID] = e
		}
	}
	return result, entries, nil
}
