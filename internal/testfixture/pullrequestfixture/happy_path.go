package pullrequestfixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// These review cases produce varied, readable changes in the manual scenario.
// The baseline checklist is replaced by structured data and a browser guide.
var happyPathReviewCases = []struct {
	Name     string `json:"name"`
	View     string `json:"view"`
	Action   string `json:"action"`
	Expected string `json:"expected"`
}{
	{"Create a pull request", "changes", "Create an open pull request from the source Holon", "The pull request overview opens"},
	{"Watch generation", "overview", "Inspect the generation tile while it is running", "The title and running state remain visible"},
	{"Inspect operations", "overview", "Hover over the generation tile", "Operations scrolls to the active generation Holon"},
	{"Browse commits", "commits", "Open each of the five commits", "Each commit has a distinct subject and diff"},
	{"Review additions", "changes", "Open the review configuration and browser assets", "Added lines are highlighted in the diff"},
	{"Review deletions", "changes", "Open the retired legacy checklist", "Removed lines are highlighted in the diff"},
	{"Read the description", "overview", "Wait for generation to complete", "The generated Markdown replaces the generation tile"},
	{"Edit the description", "overview", "Save a change using the edit button", "The saved description includes the edit"},
	{"Cancel an edit", "overview", "Change the description and cancel", "The previously saved description remains intact"},
	{"Check publication", "overview", "Wait for publication to finish", "The pull request is open and publication has completed"},
	{"Start a rebase", "overview", "Rebase onto the updated main branch", "A rebase Holon starts with a README conflict"},
	{"Inspect the conflict", "operations", "Open the running rebase Holon", "The workspace shows the conflicting README change"},
	{"Review the resolution", "changes", "Wait for the rebase Holon to finish", "The README preserves both changes"},
	{"Repeat the rebase", "overview", "Rebase again after completion", "Another conflicting base update is resolved"},
}

func seedHappyPathBase(repositoryPath string) error {
	var legacy strings.Builder
	legacy.WriteString("# Legacy pull request review checklist\n")
	for _, review := range happyPathReviewCases {
		fmt.Fprintf(&legacy, "\n## %s\n\nView: %s\nAction: %s\nExpected: %s\nStatus: manual verification required\n",
			review.Name, review.View, review.Action, review.Expected)
	}
	return os.WriteFile(filepath.Join(repositoryPath, "legacy-review.md"), []byte(legacy.String()), 0o600)
}

// Add four commits after the initial README/scenario change. Keep the README
// change in that first commit so the rebase walkthrough has only one conflict.
func seedHappyPathCommits(worktreePath string, firstCommitAt time.Time) error {
	configuration, err := json.MarshalIndent(happyPathReviewCases, "", "  ")
	if err != nil {
		return err
	}
	var guide strings.Builder
	guide.WriteString("# Browser pull request review\n\n")
	for _, review := range happyPathReviewCases {
		fmt.Fprintf(&guide, "## %s\n\n- Open **%s** and %s.\n- Confirm: %s.\n\n",
			review.Name, review.View, review.Action, review.Expected)
	}
	commits := []struct {
		path, content, subject, body string
	}{
		{
			path: "review.json", content: string(configuration) + "\n",
			subject: "Replace the legacy review checklist with structured review cases",
			body: `Move the manual review cases into JSON with a name, view, action, and
expected result for each step. Remove the old Markdown checklist so the
browser can render and filter the same review data consistently.

Cover creation, generation, publication, description editing, and repeated
rebases in one set of cases.`,
		},
		{
			path: "review.js", content: happyPathReviewScript,
			subject: "Render review cases with progress tracking and view filtering",
			body: `Load the structured review cases and render a checklist with a completion
counter. Let reviewers filter by pull request view while retaining checked
items when they switch between views.

Use labeled controls and a status announcement so progress remains clear
when navigating the checklist with a keyboard or screen reader.`,
		},
		{
			path: "review.css", content: happyPathReviewStyles,
			subject: "Style the review checklist for wide and narrow screens",
			body: `Arrange review cases in two columns on wider screens and a single column
on narrow screens. Add spacing, borders, and readable labels so long review
instructions are easy to scan.

Keep checkboxes from shrinking and give the completion counter a distinct
visual weight as reviewers work through the cases.`,
		},
		{
			path: "REVIEW.md", content: guide.String(),
			subject: "Document the full pull request review and repeated rebase walkthrough",
			body: `Add a browser review guide that pairs each action with its expected result.
Describe how to inspect all five commits, review added and removed files,
and follow description generation through publication.

Include the README conflict and repeated rebase steps so reviewers can
exercise the complete workflow from the same seeded repository.`,
		},
	}
	for index, commit := range commits {
		if index == 0 {
			if _, err := gitOutput(worktreePath, "rm", "legacy-review.md"); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(worktreePath, commit.path), []byte(commit.content), 0o600); err != nil {
			return err
		}
		// Spread author timestamps one hour apart so the commit order is visible.
		authoredAt := firstCommitAt.Add(time.Duration(index+1) * time.Hour)
		for _, args := range [][]string{
			{"add", commit.path},
			{"-c", "user.name=Holark Browser Scenario", "-c", "user.email=browser-scenario@invalid", "commit", "--date", authoredAt.Format(time.RFC3339), "-m", commit.subject, "-m", commit.body},
		} {
			if _, err := gitOutput(worktreePath, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

const happyPathReviewScript = `export async function loadReviewCases(url = "./review.json") {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error("Could not load the pull request review checklist");
  }
  return response.json();
}

export function mountReviewChecklist(container, cases) {
  const completed = new Set();
  const progress = document.createElement("p");
  progress.className = "review-progress";
  progress.setAttribute("role", "status");

  const filter = document.createElement("select");
  filter.setAttribute("aria-label", "Filter by pull request view");
  for (const view of ["all", ...new Set(cases.map(item => item.view))]) {
    const option = document.createElement("option");
    option.value = view;
    option.textContent = view;
    filter.append(option);
  }

  const list = document.createElement("ul");
  list.className = "review-checklist";
  function render() {
    progress.textContent = completed.size + " of " + cases.length + " checks complete";
    list.replaceChildren();
    for (const item of cases) {
      if (filter.value !== "all" && item.view !== filter.value) continue;
      const row = document.createElement("li");
      const label = document.createElement("label");
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = completed.has(item.name);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) completed.add(item.name);
        else completed.delete(item.name);
        progress.textContent = completed.size + " of " + cases.length + " checks complete";
      });
      const text = document.createElement("span");
      text.textContent = item.name + ": " + item.action + ". " + item.expected + ".";
      label.append(checkbox, text);
      row.append(label);
      list.append(row);
    }
  }
  filter.addEventListener("change", render);
  container.replaceChildren(filter, progress, list);
  render();
}
`

const happyPathReviewStyles = `.review-checklist {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
  padding: 0;
  list-style: none;
}

.review-checklist li {
  border: 1px solid #d1d5db;
  border-radius: 0.5rem;
  padding: 1rem;
}

.review-checklist label {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  line-height: 1.5;
  cursor: pointer;
}

.review-checklist input {
  flex-shrink: 0;
  accent-color: #2563eb;
}

.review-progress {
  color: #374151;
  font-weight: 600;
}

@media (max-width: 640px) {
  .review-checklist {
    grid-template-columns: 1fr;
  }
}
`
