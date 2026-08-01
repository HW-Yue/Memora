package hostcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/skillcontract"
)

const (
	ContractVersion   = "memora.real-host-contract/v1"
	TaskVersion       = "memora.real-host-task/v1"
	InvocationVersion = "memora.real-host-invocation/v1"
	ReceiptVersion    = "memora.real-host-receipt/v1"
)

var canonicalCommands = []string{
	"assimilate", "doctor", "exec", "feedback", "maintain", "mutate", "query", "reflect", "schema",
}

type Contract struct {
	Version           string            `json:"version"`
	TaskVersion       string            `json:"task_version"`
	InvocationVersion string            `json:"invocation_version"`
	ReceiptVersion    string            `json:"receipt_version"`
	SkillVersion      string            `json:"skill_version"`
	AllowedCommands   []string          `json:"allowed_commands"`
	HostSurfaces      []HostSurface     `json:"host_surfaces"`
	ProviderExamples  []ProviderExample `json:"provider_examples"`
	Budgets           Limits            `json:"budgets"`
	Credentials       string            `json:"credentials"`
	EndpointEvidence  string            `json:"endpoint_evidence"`
	HiddenReasoning   string            `json:"hidden_reasoning"`
	PromptPersistence string            `json:"prompt_persistence"`
}

type HostSurface struct {
	Name              string   `json:"name"`
	ProviderProtocols []string `json:"provider_protocols"`
}

type ProviderExample struct {
	Name      string   `json:"name"`
	Owner     string   `json:"owner"`
	Protocols []string `json:"protocols"`
}

type Limits struct {
	MaxPromptCharacters    uint64 `json:"max_prompt_characters"`
	MaxAuthorizedDatabases uint64 `json:"max_authorized_databases"`
	MaxToolCalls           uint64 `json:"max_tool_calls"`
	MaxContextCharacters   uint64 `json:"max_context_characters"`
	MaxElapsedMillis       uint64 `json:"max_elapsed_ms"`
}

type Budget struct {
	MaxToolCalls         uint64 `json:"max_tool_calls"`
	MaxContextCharacters uint64 `json:"max_context_characters"`
	MaxElapsedMillis     uint64 `json:"max_elapsed_ms"`
}

type Task struct {
	Version             string   `json:"version"`
	ID                  string   `json:"task_id"`
	Prompt              string   `json:"prompt"`
	AuthorizedDatabases []string `json:"authorized_databases"`
	CorpusDigest        string   `json:"corpus_digest"`
	Budget              Budget   `json:"budget"`
}

type HostProfile struct {
	Surface      string `json:"surface"`
	Provider     string `json:"provider"`
	Protocol     string `json:"protocol"`
	Model        string `json:"model"`
	EndpointHost string `json:"endpoint_host"`
}

type Invocation struct {
	Version        string      `json:"version"`
	TaskDigest     string      `json:"task_digest"`
	SkillDigest    string      `json:"skill_digest"`
	ProtocolDigest string      `json:"protocol_digest"`
	Host           HostProfile `json:"host"`
}

type ToolCall struct {
	Sequence          uint64 `json:"sequence"`
	Command           string `json:"command"`
	ResultDigest      string `json:"result_digest"`
	ContextCharacters uint64 `json:"context_characters"`
	ElapsedMillis     uint64 `json:"elapsed_ms"`
}

type Usage struct {
	InputTokens       uint64 `json:"input_tokens"`
	OutputTokens      uint64 `json:"output_tokens"`
	ContextCharacters uint64 `json:"context_characters"`
	ElapsedMillis     uint64 `json:"elapsed_ms"`
}

type Receipt struct {
	Version        string      `json:"version"`
	TaskDigest     string      `json:"task_digest"`
	SkillDigest    string      `json:"skill_digest"`
	ProtocolDigest string      `json:"protocol_digest"`
	Host           HostProfile `json:"host"`
	Status         string      `json:"status"`
	AnswerDigest   string      `json:"answer_digest,omitempty"`
	ToolCalls      []ToolCall  `json:"tool_calls"`
	Usage          Usage       `json:"usage"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "real host contract: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func Load(path string) (Contract, string, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, "", contractError(result.CodeInternal, "read contract: %v", err)
	}
	var contract Contract
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, "", contractError(result.CodeValidation, "decode contract: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Contract{}, "", contractError(result.CodeValidation, "decode contract: %v", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, "", err
	}
	return contract, digestBytes(encoded), nil
}

func (contract Contract) Validate() error {
	if contract.Version != ContractVersion || contract.TaskVersion != TaskVersion ||
		contract.InvocationVersion != InvocationVersion || contract.ReceiptVersion != ReceiptVersion ||
		contract.SkillVersion != skillcontract.Version {
		return contractError(result.CodeValidation, "version binding is invalid")
	}
	if !sameStrings(contract.AllowedCommands, canonicalCommands) {
		return contractError(result.CodeValidation, "allowed commands differ from the Canonical Skill")
	}
	if contract.Budgets != (Limits{
		MaxPromptCharacters: 4096, MaxAuthorizedDatabases: 32, MaxToolCalls: 64,
		MaxContextCharacters: 12000, MaxElapsedMillis: 600000,
	}) {
		return contractError(result.CodeValidation, "frozen budgets are invalid")
	}
	if contract.Credentials != "never-passed-to-memora" || contract.EndpointEvidence != "hostname-only" ||
		contract.HiddenReasoning != "forbidden" || contract.PromptPersistence != "benchmark-artifact-only" {
		return contractError(result.CodeValidation, "privacy boundary is invalid")
	}
	if len(contract.HostSurfaces) != 2 || len(contract.ProviderExamples) != 1 {
		return contractError(result.CodeValidation, "host/provider matrix is incomplete")
	}
	surfaces := map[string]string{}
	for _, surface := range contract.HostSurfaces {
		if surface.Name == "" || len(surface.ProviderProtocols) != 1 || surfaces[surface.Name] != "" {
			return contractError(result.CodeValidation, "host surface is invalid or duplicated")
		}
		surfaces[surface.Name] = surface.ProviderProtocols[0]
	}
	if surfaces["codex"] != "openai-compatible" || surfaces["claude-code"] != "anthropic-compatible" {
		return contractError(result.CodeValidation, "host protocol mapping is invalid")
	}
	provider := contract.ProviderExamples[0]
	if provider.Name != "kimi" || provider.Owner != "host" ||
		!sameStrings(provider.Protocols, []string{"anthropic-compatible", "openai-compatible"}) {
		return contractError(result.CodeValidation, "Kimi Provider boundary is invalid")
	}
	return nil
}

func (contract Contract) TaskDigest(task Task) (string, error) {
	if err := contract.validateTask(task); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		return "", contractError(result.CodeInternal, "encode Task: %v", err)
	}
	return digestBytes(encoded), nil
}

func (contract Contract) DecodeTask(encoded []byte) (Task, error) {
	var task Task
	if err := decodeStrict(encoded, &task); err != nil {
		return Task{}, contractError(result.CodeValidation, "decode Task: %v", err)
	}
	if err := contract.validateTask(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (contract Contract) DecodeReceipt(task Task, encoded []byte) (Receipt, error) {
	var receipt Receipt
	if err := decodeStrict(encoded, &receipt); err != nil {
		return Receipt{}, contractError(result.CodeValidation, "decode receipt: %v", err)
	}
	if err := contract.ValidateReceipt(task, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (contract Contract) ValidateMatrix(invocations []Invocation) error {
	if err := contract.Validate(); err != nil {
		return err
	}
	if len(invocations) < 3 {
		return contractError(result.CodeValidation, "host matrix requires Codex, Claude Code, and Kimi")
	}
	var taskDigest, skillDigest, protocolDigest string
	seenProfiles := map[string]bool{}
	foundCodex, foundClaude, foundKimi := false, false, false
	for index, invocation := range invocations {
		if invocation.Version != InvocationVersion || !validDigest(invocation.TaskDigest) ||
			!validDigest(invocation.SkillDigest) || !validDigest(invocation.ProtocolDigest) {
			return contractError(result.CodeValidation, "invocation %d identity is invalid", index)
		}
		if err := contract.validateHost(invocation.Host); err != nil {
			return err
		}
		if index == 0 {
			taskDigest, skillDigest, protocolDigest = invocation.TaskDigest, invocation.SkillDigest, invocation.ProtocolDigest
		} else if invocation.TaskDigest != taskDigest || invocation.SkillDigest != skillDigest ||
			invocation.ProtocolDigest != protocolDigest {
			return contractError(result.CodeValidation, "host matrix digest binding differs")
		}
		profileKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", invocation.Host.Surface,
			invocation.Host.Provider, invocation.Host.Protocol, invocation.Host.Model, invocation.Host.EndpointHost)
		if seenProfiles[profileKey] {
			return contractError(result.CodeValidation, "host matrix profile is duplicated")
		}
		seenProfiles[profileKey] = true
		foundCodex = foundCodex || invocation.Host.Surface == "codex"
		foundClaude = foundClaude || invocation.Host.Surface == "claude-code"
		foundKimi = foundKimi || strings.EqualFold(invocation.Host.Provider, "kimi")
	}
	if !foundCodex || !foundClaude || !foundKimi {
		return contractError(result.CodeValidation, "host matrix requires Codex, Claude Code, and kimi Provider")
	}
	return nil
}

func (contract Contract) ValidateReceipt(task Task, receipt Receipt) error {
	taskDigest, err := contract.TaskDigest(task)
	if err != nil {
		return err
	}
	if receipt.Version != ReceiptVersion || receipt.TaskDigest != taskDigest ||
		!validDigest(receipt.SkillDigest) || !validDigest(receipt.ProtocolDigest) {
		return contractError(result.CodeValidation, "receipt digest binding is invalid")
	}
	if err := contract.validateHost(receipt.Host); err != nil {
		return err
	}
	switch receipt.Status {
	case "completed", "failed", "cancelled", "budget_exhausted":
	default:
		return contractError(result.CodeValidation, "receipt status is invalid")
	}
	if receipt.Status == "completed" && !validDigest(receipt.AnswerDigest) {
		return contractError(result.CodeValidation, "completed receipt answer digest is invalid")
	}
	if receipt.Status != "completed" && receipt.AnswerDigest != "" && !validDigest(receipt.AnswerDigest) {
		return contractError(result.CodeValidation, "receipt answer digest is invalid")
	}
	if receipt.ToolCalls == nil || uint64(len(receipt.ToolCalls)) > task.Budget.MaxToolCalls {
		return contractError(result.CodeValidation, "receipt exceeds tool call budget")
	}
	allowed := make(map[string]bool, len(contract.AllowedCommands))
	for _, command := range contract.AllowedCommands {
		allowed[command] = true
	}
	var contextCharacters, elapsedMillis uint64
	for index, call := range receipt.ToolCalls {
		if call.Sequence != uint64(index+1) || !allowed[call.Command] || !validDigest(call.ResultDigest) {
			return contractError(result.CodeValidation, "receipt tool call %d is invalid", index)
		}
		if ^uint64(0)-contextCharacters < call.ContextCharacters || ^uint64(0)-elapsedMillis < call.ElapsedMillis {
			return contractError(result.CodeValidation, "receipt usage counter overflowed")
		}
		contextCharacters += call.ContextCharacters
		elapsedMillis += call.ElapsedMillis
	}
	if receipt.Usage.ContextCharacters > task.Budget.MaxContextCharacters ||
		receipt.Usage.ElapsedMillis > task.Budget.MaxElapsedMillis ||
		contextCharacters > receipt.Usage.ContextCharacters || elapsedMillis > receipt.Usage.ElapsedMillis {
		return contractError(result.CodeValidation, "receipt exceeds or contradicts Task budget")
	}
	return nil
}

func (contract Contract) validateTask(task Task) error {
	if err := contract.Validate(); err != nil {
		return err
	}
	if task.Version != TaskVersion || !validSlug(task.ID, 128) || !validText(task.Prompt, int(contract.Budgets.MaxPromptCharacters)) ||
		!validDigest(task.CorpusDigest) {
		return contractError(result.CodeValidation, "Task identity, prompt, or corpus digest is invalid")
	}
	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:real-host-contract",
		AuthorizedDatabases: task.AuthorizedDatabases,
	}
	if len(task.AuthorizedDatabases) > int(contract.Budgets.MaxAuthorizedDatabases) || authorization.Validate() != nil {
		return contractError(result.CodeValidation, "Task Database scope is invalid")
	}
	budget := task.Budget
	if budget.MaxToolCalls == 0 || budget.MaxContextCharacters == 0 || budget.MaxElapsedMillis == 0 ||
		budget.MaxToolCalls > contract.Budgets.MaxToolCalls ||
		budget.MaxContextCharacters > contract.Budgets.MaxContextCharacters ||
		budget.MaxElapsedMillis > contract.Budgets.MaxElapsedMillis {
		return contractError(result.CodeValidation, "Task budget is invalid")
	}
	return nil
}

func (contract Contract) validateHost(host HostProfile) error {
	if !validText(host.Provider, 80) || !validText(host.Model, 160) || !validHostname(host.EndpointHost) {
		return contractError(result.CodeValidation, "host profile metadata or endpoint hostname is invalid")
	}
	for _, surface := range contract.HostSurfaces {
		if surface.Name == host.Surface {
			for _, protocol := range surface.ProviderProtocols {
				if protocol == host.Protocol {
					return nil
				}
			}
			return contractError(result.CodeValidation, "host Provider protocol is incompatible with surface")
		}
	}
	return contractError(result.CodeValidation, "host surface is unsupported")
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] || index > 0 && leftCopy[index] == leftCopy[index-1] {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validSlug(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func validHostname(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.Trim(value, ".") != value ||
		strings.ContainsAny(value, ":/@?#[]\\ \t\r\n") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func digestBytes(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeStrict[T any](encoded []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func contractError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
