package upload

// uploadDeadlines keeps selection and per-path replacement logarithmic in
// the backlog size. All access is guarded by Service.scheduledMu.
type uploadDeadlines []*scheduledUpload

func (h uploadDeadlines) Len() int { return len(h) }
func (h uploadDeadlines) Less(i, j int) bool {
	return h[i].deadline.Before(h[j].deadline)
}
func (h uploadDeadlines) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *uploadDeadlines) Push(value any) {
	task := value.(*scheduledUpload)
	task.index = len(*h)
	*h = append(*h, task)
}
func (h *uploadDeadlines) Pop() any {
	old := *h
	task := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	task.index = -1
	return task
}
