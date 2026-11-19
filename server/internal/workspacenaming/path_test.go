package workspacenaming

import (
	"encoding/json"
	"strings"
	"testing"
)

func reservedTaskDirectories() []string {
	names := []string{"CON", "PRN", "AUX", "NUL"}
	for digit := '1'; digit <= '9'; digit++ {
		names = append(names, "COM"+string(digit), "LPT"+string(digit))
	}
	names = append(names, "con.txt", "PrN.tar.gz", "aUx", "nul.log", "cOm1.txt", "lPt9.tar.gz", "NUL .txt", "COM\u00b9", "LPT\u00b2.txt", "com\u00b3")
	paths := []string{}
	for _, name := range names {
		paths = append(paths, name, name+"/{task-id}", "group/"+name+"/{task-id}", "{task-id}/"+name)
	}
	return paths
}

func portableTaskDirectories() []string {
	return []string{"{task-id}/console", "{task-id}/NUL-backup", "{task-id}/AUX_1", "{task-id}/COM0", "{task-id}/COM10.txt", "{task-id}/LPT0", "{task-id}/LPT10", "{task-id}/COM1x.txt", "{task-id}/prefix.NUL", "{task-id}/.NUL", "{task-id}/_cache", "{task-id}/-notes", "{task-id}/notes v2", "{task-id}/caf\u00e9", "{task-id}/" + strings.Repeat("a", 80)}
}

func TestTaskDirectoryReservedNames(t *testing.T) {
	current := Rules{TaskDirectoryTemplate: "task-{task-id}", BranchTemplate: "custom/{task-id}"}
	for _, path := range reservedTaskDirectories() {
		t.Run(path, func(t *testing.T) {
			want := Problem{Code: "path_invalid", Field: "task_directory_template", Severity: "error"}
			if err := Validate(Rules{TaskDirectoryTemplate: path}); err != want {
				t.Errorf("Validate accepted reserved task path %q: %v", path, err)
			}
			raw, _ := json.Marshal(Rules{TaskDirectoryTemplate: path})
			if got, err := Patch(current, raw); err != want || got != current {
				t.Errorf("invalid path changed saved rules: %+v, %v", got, err)
			}
		})
	}
	for _, path := range portableTaskDirectories() {
		if err := Validate(Rules{TaskDirectoryTemplate: path}); err != nil {
			t.Errorf("rejected ordinary task path %q: %v", path, err)
		}
	}
}
