package webui

import "sort"

func sortAssignmentGroups(groups []assignmentGroupView) {
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
}
