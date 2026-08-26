package routecapability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
)

func (card *PriceCard) Seal() error {
	if card == nil {
		return capabilityError(result.CodeInternal, "price card is nil")
	}
	if err := card.validateContent(); err != nil {
		return err
	}
	hash, err := priceCardDigest(*card)
	if err != nil {
		return err
	}
	card.Hash = hash
	return nil
}

func (card PriceCard) Validate() error {
	if err := card.validateContent(); err != nil {
		return err
	}
	if !validDigest(card.Hash) {
		return capabilityError(result.CodeValidation, "price card hash is invalid")
	}
	want, err := priceCardDigest(card)
	if err != nil {
		return err
	}
	if card.Hash != want {
		return capabilityError(result.CodeValidation, "price card hash mismatch")
	}
	return nil
}

func (card PriceCard) validateContent() error {
	if card.Version != PriceCardVersion || strings.TrimSpace(card.ID) == "" || len(card.Entries) == 0 {
		return capabilityError(result.CodeValidation, "price card version, ID, and entries are required")
	}
	seen, previous := map[string]bool{}, ""
	for _, entry := range card.Entries {
		key := entry.Provider + "\x00" + entry.Model
		if strings.TrimSpace(entry.Provider) == "" || strings.TrimSpace(entry.Model) == "" ||
			entry.InputMicroUSDPerMillion == 0 || entry.OutputMicroUSDPerMillion == 0 ||
			entry.InputMicroUSDPerMillion > 1_000_000_000_000 ||
			entry.OutputMicroUSDPerMillion > 1_000_000_000_000 || seen[key] || previous > key {
			return capabilityError(result.CodeValidation, "price entries must be unique, sorted, and bounded")
		}
		seen[key], previous = true, key
	}
	return nil
}

func (report *Report) Seal() error {
	if report == nil {
		return capabilityError(result.CodeInternal, "report is nil")
	}
	if err := report.validateContent(); err != nil {
		return err
	}
	hash, err := reportDigest(*report)
	if err != nil {
		return err
	}
	report.Hash = hash
	return nil
}

func (report Report) Validate() error {
	if !validDigest(report.Hash) {
		return capabilityError(result.CodeValidation, "report hash is invalid")
	}
	want, err := reportDigest(report)
	if err != nil {
		return err
	}
	if report.Hash != want {
		return capabilityError(result.CodeValidation, "report hash mismatch")
	}
	return report.validateContent()
}

func (report Report) validateContent() error {
	if report.Version != ReportVersion || report.PolicyVersion != PolicyVersion ||
		report.Decision.PolicyVersion != PolicyVersion || report.CorpusID == "" ||
		!validDigest(report.CorpusDigest) || !validDigest(report.ContractDigest) ||
		!validDigest(report.SkillDigest) || !validDigest(report.ProtocolDigest) ||
		len(report.SourceReports) == 0 || len(report.Buckets) == 0 || len(report.Decision.Arms) != 4 {
		return capabilityError(result.CodeValidation, "report identity, sources, buckets, and decision are required")
	}
	if report.PriceCardDigest != "" && !validDigest(report.PriceCardDigest) {
		return capabilityError(result.CodeValidation, "price card digest is invalid")
	}
	previous, sources := "", map[string]bool{}
	for _, source := range report.SourceReports {
		if !validDigest(source.Hash) || source.Runs == 0 || sources[source.Hash] || previous > source.Hash {
			return capabilityError(result.CodeValidation, "source report references are invalid")
		}
		sources[source.Hash], previous = true, source.Hash
	}
	seenBuckets := map[string]bool{}
	wantSlices := map[string]bool{"aggregate": false, "fanout": false, "depth": false,
		"difficulty": false, "language": false, "host_model": false}
	armSlices := map[routebenchmarkrunner.Arm]map[string]bool{}
	for _, arm := range routebenchmarkrunner.Arms() {
		armSlices[arm] = map[string]bool{}
	}
	for _, bucket := range report.Buckets {
		identity := bucketIdentity(bucket.Key)
		if bucket.Key.Value == "" || seenBuckets[identity] {
			return capabilityError(result.CodeValidation, "bucket identity is invalid or duplicated")
		}
		if _, ok := wantSlices[bucket.Key.Slice]; !ok {
			return capabilityError(result.CodeValidation, "bucket slice %q is unsupported", bucket.Key.Slice)
		}
		if armSlices[bucket.Key.Arm] == nil {
			return capabilityError(result.CodeValidation, "bucket arm %q is unsupported", bucket.Key.Arm)
		}
		seenBuckets[identity], wantSlices[bucket.Key.Slice] = true, true
		armSlices[bucket.Key.Arm][bucket.Key.Slice] = true
	}
	for name, found := range wantSlices {
		if !found {
			return capabilityError(result.CodeValidation, "bucket slice %q is missing", name)
		}
	}
	for arm, slices := range armSlices {
		for name := range wantSlices {
			if !slices[name] {
				return capabilityError(result.CodeValidation, "arm %q omits slice %q", arm, name)
			}
		}
	}
	if !containsArm(routebenchmarkrunner.Arms(), report.Decision.DefaultArm) {
		return capabilityError(result.CodeValidation, "default arm is invalid")
	}
	wantDecisionOrder := []routebenchmarkrunner.Arm{
		routebenchmarkrunner.ArmHybridPrefetch, routebenchmarkrunner.ArmHybrid,
		routebenchmarkrunner.ArmVector, routebenchmarkrunner.ArmLexical,
	}
	for index, decision := range report.Decision.Arms {
		if decision.Arm != wantDecisionOrder[index] || decision.Reasons == nil ||
			(decision.Eligible && len(decision.Reasons) != 0) {
			return capabilityError(result.CodeValidation, "arm decision order or reasons are invalid")
		}
	}
	wantDefault := routebenchmarkrunner.ArmRouter
	for _, decision := range report.Decision.Arms {
		if wantDefault == routebenchmarkrunner.ArmRouter && decision.Eligible {
			wantDefault = decision.Arm
		}
	}
	if report.Decision.DefaultArm != wantDefault {
		return capabilityError(result.CodeValidation, "default arm differs from eligible decision order")
	}
	return nil
}

func (report Report) ValidateAgainst(
	corpus routebenchmark.Corpus,
	contract hostcontract.Contract,
	contractDigest string,
	sources []routebenchmarkrunner.Report,
	priceCard *PriceCard,
) error {
	if err := report.Validate(); err != nil {
		return err
	}
	want, err := Build(corpus, contract, contractDigest, sources, priceCard)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report, want) {
		return capabilityError(result.CodeValidation, "report differs from source evidence")
	}
	return nil
}

func Encode(report Report) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, capabilityError(result.CodeInternal, "encode report: %v", err)
	}
	return append(encoded, '\n'), nil
}

func Decode(encoded []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, capabilityError(result.CodeValidation, "decode report: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Report{}, capabilityError(result.CodeValidation, "decode report: trailing JSON value")
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func Load(path string) (Report, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Report{}, capabilityError(result.CodeInternal, "read report: %v", err)
	}
	return Decode(encoded)
}

func LoadPriceCard(path string) (PriceCard, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return PriceCard{}, capabilityError(result.CodeInternal, "read price card: %v", err)
	}
	var card PriceCard
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&card); err != nil {
		return PriceCard{}, capabilityError(result.CodeValidation, "decode price card: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return PriceCard{}, capabilityError(result.CodeValidation, "decode price card: trailing JSON value")
	}
	if err := card.Validate(); err != nil {
		return PriceCard{}, err
	}
	return card, nil
}

func WriteAtomic(path string, report Report) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return capabilityError(result.CodeValidation, "output path must be absolute and normalized")
	}
	encoded, err := Encode(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return capabilityError(result.CodeInternal, "create output directory: %v", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".route-capability-*.json")
	if err != nil {
		return capabilityError(result.CodeInternal, "create staging report: %v", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func reportDigest(report Report) (string, error) {
	report.Hash = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", capabilityError(result.CodeInternal, "encode report identity: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func priceCardDigest(card PriceCard) (string, error) {
	card.Hash = ""
	encoded, err := json.Marshal(card)
	if err != nil {
		return "", capabilityError(result.CodeInternal, "encode price card identity: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsArm(values []routebenchmarkrunner.Arm, target routebenchmarkrunner.Arm) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
