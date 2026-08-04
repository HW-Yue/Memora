package agent

import "sort"

const (
	AssimilationBranchIssueVersion    = "memora.assimilation-branch-issue/v1"
	AssimilationBranchAnswerVersion   = "memora.assimilation-branch-answer/v1"
	AssimilationBranchSnapshotVersion = "memora.assimilation-branch-snapshot/v1"
)

type AssimilationBranchState string

const (
	AssimilationBranchWaitingUser AssimilationBranchState = "waiting_user"
	AssimilationBranchReady       AssimilationBranchState = "ready"
)

type AssimilationBranchIssue struct {
	Version        string   `json:"version"`
	BranchID       string   `json:"branch_id"`
	IssueID        string   `json:"issue_id"`
	ExtentSequence uint64   `json:"extent_sequence"`
	ExtentSHA256   string   `json:"extent_sha256"`
	ClaimIDs       []string `json:"claim_ids,omitempty"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	Question       string   `json:"question"`
	Options        []string `json:"options"`
}

type AssimilationBranchAnswer struct {
	Version         string `json:"version"`
	BranchID        string `json:"branch_id"`
	IssueID         string `json:"issue_id"`
	SelectedOption  string `json:"selected_option"`
	AnswerUTF8Bytes uint64 `json:"answer_utf8_bytes"`
	AnswerSHA256    string `json:"answer_sha256"`
}

type AssimilationBranchSnapshot struct {
	Version        string                    `json:"version"`
	BranchID       string                    `json:"branch_id"`
	Revision       uint64                    `json:"revision"`
	State          AssimilationBranchState   `json:"state"`
	ExtentSequence uint64                    `json:"extent_sequence"`
	ExtentSHA256   string                    `json:"extent_sha256"`
	Issue          *AssimilationBranchIssue  `json:"issue,omitempty"`
	Resolution     *AssimilationBranchAnswer `json:"resolution,omitempty"`
}

func (branch AssimilationBranchSnapshot) Validate() error {
	if branch.Version != AssimilationBranchSnapshotVersion ||
		!validSourceIntakeID(branch.BranchID, 160) || branch.Revision == 0 ||
		branch.ExtentSequence == 0 || !validSourceSHA256(branch.ExtentSHA256) {
		return ErrAssimilationJobInvalid
	}
	switch branch.State {
	case AssimilationBranchWaitingUser:
		if branch.Issue == nil || validAssimilationBranchIssue(*branch.Issue) != nil ||
			branch.Issue.BranchID != branch.BranchID ||
			branch.Issue.ExtentSequence != branch.ExtentSequence ||
			branch.Issue.ExtentSHA256 != branch.ExtentSHA256 ||
			(branch.Resolution != nil && (validAssimilationBranchAnswer(*branch.Resolution) != nil ||
				branch.Resolution.BranchID != branch.BranchID)) {
			return ErrAssimilationJobInvalid
		}
	case AssimilationBranchReady:
		if branch.Revision < 2 || branch.Issue != nil || branch.Resolution == nil ||
			validAssimilationBranchAnswer(*branch.Resolution) != nil ||
			branch.Resolution.BranchID != branch.BranchID {
			return ErrAssimilationJobInvalid
		}
	default:
		return ErrAssimilationJobInvalid
	}
	return nil
}

func (snapshot AssimilationJobSnapshot) Branch(branchID string) (AssimilationBranchSnapshot, bool) {
	if !validSourceIntakeID(branchID, 160) {
		return AssimilationBranchSnapshot{}, false
	}
	index, found := assimilationBranchIndex(snapshot.Branches, branchID)
	if !found {
		return AssimilationBranchSnapshot{}, false
	}
	return cloneAssimilationBranch(snapshot.Branches[index]), true
}

func (snapshot AssimilationJobSnapshot) BranchRunnable(branchID string) bool {
	if snapshot.State != AssimilationJobActive || !validSourceIntakeID(branchID, 160) {
		return false
	}
	branch, found := snapshot.Branch(branchID)
	return !found || branch.State == AssimilationBranchReady
}

func deriveAssimilationBranchEvent(
	snapshot AssimilationJobSnapshot,
	command AssimilationJobCommand,
	commandSHA256 string,
) (AssimilationJobEvent, AssimilationJobSnapshot, error) {
	if snapshot.State != AssimilationJobActive {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	event := AssimilationJobEvent{
		Version: AssimilationJobEventVersion, JobID: command.JobID,
		Revision: snapshot.Revision + 1, CommandID: command.CommandID,
		CommandSHA256: commandSHA256, State: AssimilationJobActive,
	}
	next := cloneAssimilationJobSnapshot(snapshot)
	switch command.Kind {
	case AssimilationJobCommandRaiseBranchIssue:
		issue := *command.BranchIssue
		index, found := assimilationBranchIndex(next.Branches, issue.BranchID)
		branchRevision := uint64(1)
		branch := AssimilationBranchSnapshot{
			Version: AssimilationBranchSnapshotVersion, BranchID: issue.BranchID,
			Revision: branchRevision, State: AssimilationBranchWaitingUser,
			ExtentSequence: issue.ExtentSequence, ExtentSHA256: issue.ExtentSHA256,
			Issue: cloneAssimilationBranchIssuePointer(&issue),
		}
		if found {
			current := next.Branches[index]
			if command.ExpectedBranchRevision != current.Revision {
				return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationBranchRevision
			}
			if current.State != AssimilationBranchReady || current.ExtentSequence != issue.ExtentSequence ||
				current.ExtentSHA256 != issue.ExtentSHA256 {
				return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
			}
			branchRevision = current.Revision + 1
			branch.Revision = branchRevision
			branch.Resolution = cloneAssimilationBranchAnswerPointer(current.Resolution)
			next.Branches[index] = branch
		} else {
			if command.ExpectedBranchRevision != 0 {
				return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationBranchRevision
			}
			next.Branches = append(next.Branches, branch)
			sort.Slice(next.Branches, func(left, right int) bool {
				return next.Branches[left].BranchID < next.Branches[right].BranchID
			})
		}
		event.Kind = AssimilationJobEventBranchIssueRaised
		event.BranchRevision = branchRevision
		event.BranchIssue = cloneAssimilationBranchIssuePointer(&issue)
	case AssimilationJobCommandAnswerBranchIssue:
		answer := *command.BranchAnswer
		index, found := assimilationBranchIndex(next.Branches, answer.BranchID)
		if !found {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationBranchRevision
		}
		current := next.Branches[index]
		if command.ExpectedBranchRevision != current.Revision {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationBranchRevision
		}
		if current.State != AssimilationBranchWaitingUser || current.Issue == nil ||
			current.Issue.IssueID != answer.IssueID || !branchOptionAllowed(*current.Issue, answer.SelectedOption) {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
		}
		current.Revision++
		current.State = AssimilationBranchReady
		current.Issue = nil
		current.Resolution = cloneAssimilationBranchAnswerPointer(&answer)
		next.Branches[index] = current
		event.Kind = AssimilationJobEventBranchIssueAnswered
		event.BranchRevision = current.Revision
		event.BranchAnswer = cloneAssimilationBranchAnswerPointer(&answer)
	default:
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	next.Revision = event.Revision
	if event.Validate() != nil || !validAssimilationBranches(next.Branches) {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	return event, next, nil
}

func validAssimilationBranchIssue(issue AssimilationBranchIssue) error {
	if issue.Version != AssimilationBranchIssueVersion ||
		!validSourceIntakeID(issue.BranchID, 160) || !validSourceIntakeID(issue.IssueID, 160) ||
		issue.ExtentSequence == 0 || !validSourceSHA256(issue.ExtentSHA256) ||
		len(issue.ClaimIDs) > 32 || !validSourceIntakeID(issue.Code, 80) ||
		!validSourceIntakeText(issue.Message, 2000) || !validSourceIntakeText(issue.Question, 2000) ||
		len(issue.Options) < 2 || len(issue.Options) > 8 {
		return ErrAssimilationJobInvalid
	}
	claims := make(map[string]struct{}, len(issue.ClaimIDs))
	for _, claimID := range issue.ClaimIDs {
		if !validSourceIntakeID(claimID, 160) {
			return ErrAssimilationJobInvalid
		}
		if _, duplicate := claims[claimID]; duplicate {
			return ErrAssimilationJobInvalid
		}
		claims[claimID] = struct{}{}
	}
	options := make(map[string]struct{}, len(issue.Options))
	for _, option := range issue.Options {
		if !validSourceIntakeID(option, 80) {
			return ErrAssimilationJobInvalid
		}
		if _, duplicate := options[option]; duplicate {
			return ErrAssimilationJobInvalid
		}
		options[option] = struct{}{}
	}
	return nil
}

func validAssimilationBranchAnswer(answer AssimilationBranchAnswer) error {
	if answer.Version != AssimilationBranchAnswerVersion ||
		!validSourceIntakeID(answer.BranchID, 160) || !validSourceIntakeID(answer.IssueID, 160) ||
		!validSourceIntakeID(answer.SelectedOption, 80) || answer.AnswerUTF8Bytes == 0 ||
		answer.AnswerUTF8Bytes > 64<<10 || !validSourceSHA256(answer.AnswerSHA256) {
		return ErrAssimilationJobInvalid
	}
	return nil
}

func validAssimilationBranches(branches []AssimilationBranchSnapshot) bool {
	if len(branches) > 1024 {
		return false
	}
	previous := ""
	for _, branch := range branches {
		if branch.Validate() != nil || (previous != "" && branch.BranchID <= previous) {
			return false
		}
		previous = branch.BranchID
	}
	return true
}

func branchOptionAllowed(issue AssimilationBranchIssue, selected string) bool {
	for _, option := range issue.Options {
		if option == selected {
			return true
		}
	}
	return false
}

func assimilationBranchCommand(kind AssimilationJobCommandKind) bool {
	return kind == AssimilationJobCommandRaiseBranchIssue || kind == AssimilationJobCommandAnswerBranchIssue
}

func commandHasNoBranchPayload(command AssimilationJobCommand) bool {
	return command.ExpectedBranchRevision == 0 && command.BranchIssue == nil && command.BranchAnswer == nil
}

func eventHasNoBranchPayload(event AssimilationJobEvent) bool {
	return event.BranchRevision == 0 && event.BranchIssue == nil && event.BranchAnswer == nil
}

func assimilationBranchIndex(branches []AssimilationBranchSnapshot, branchID string) (int, bool) {
	index := sort.Search(len(branches), func(index int) bool {
		return branches[index].BranchID >= branchID
	})
	return index, index < len(branches) && branches[index].BranchID == branchID
}

func cloneAssimilationBranches(branches []AssimilationBranchSnapshot) []AssimilationBranchSnapshot {
	if branches == nil {
		return nil
	}
	cloned := make([]AssimilationBranchSnapshot, len(branches))
	for index, branch := range branches {
		cloned[index] = cloneAssimilationBranch(branch)
	}
	return cloned
}

func cloneAssimilationBranch(branch AssimilationBranchSnapshot) AssimilationBranchSnapshot {
	branch.Issue = cloneAssimilationBranchIssuePointer(branch.Issue)
	branch.Resolution = cloneAssimilationBranchAnswerPointer(branch.Resolution)
	return branch
}

func cloneAssimilationBranchIssuePointer(issue *AssimilationBranchIssue) *AssimilationBranchIssue {
	if issue == nil {
		return nil
	}
	cloned := *issue
	cloned.ClaimIDs = append([]string(nil), issue.ClaimIDs...)
	cloned.Options = append([]string(nil), issue.Options...)
	return &cloned
}

func cloneAssimilationBranchAnswerPointer(answer *AssimilationBranchAnswer) *AssimilationBranchAnswer {
	if answer == nil {
		return nil
	}
	cloned := *answer
	return &cloned
}
