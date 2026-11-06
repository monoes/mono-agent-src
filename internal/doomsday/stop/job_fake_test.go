package stop

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
)

// ----- a fake Windows: processes, jobs, and the anchor's handle -----

type fakeWinProc struct {
	stamp, comm string
	job         string // the job the process is born in or was put in
}

type fakeWin struct {
	procs     map[int]*fakeWinProc
	jobs      map[string]*fakeWinJob
	holder    map[string]int // job name -> the anchor holding its last handle
	visible   map[string]int // a terminated job stays openable for this many more looks (handles close late)
	next      int
	self      int
	session   int
	lingers   int
	failAt    string // "create", "adopt", "anchor"
	killed    []int
	parentFor []int
	terminate int

	terminateErr, memberErr, killAnchorErr, parentErr error
	childOutside                                      bool // a child born under the anchor does NOT join the job
}

type fakeWinJob struct {
	w    *fakeWin
	name string
}

func newFakeWin() *fakeWin {
	w := &fakeWin{procs: map[int]*fakeWinProc{}, jobs: map[string]*fakeWinJob{}, holder: map[string]int{},
		visible: map[string]int{}, next: 100, self: 1}
	w.procs[1] = &fakeWinProc{stamp: "s-self", comm: "monoagentcli.exe"}
	return w
}

func (w *fakeWin) BootID() (string, error) { return "boot-w", nil }
func (w *fakeWin) Self() (int, string, error) {
	return w.self, w.procs[w.self].stamp, nil
}
func (w *fakeWin) Session() (int, error) { return w.session, nil }
func (w *fakeWin) StartAnchor() (int, string, error) {
	if w.failAt == "anchor" {
		return 0, "", errors.New("no ping.exe")
	}
	w.next++
	w.procs[w.next] = &fakeWinProc{stamp: fmt.Sprintf("s%d", w.next), comm: "ping.exe"}
	return w.next, w.procs[w.next].stamp, nil
}
func (w *fakeWin) KillAnchor(pid int, stamp string) error {
	if w.killAnchorErr != nil {
		return w.killAnchorErr
	}
	if p, ok := w.procs[pid]; ok && p.stamp == stamp {
		w.killed = append(w.killed, pid)
		w.endProc(pid)
	}
	return nil
}

// endProc ends a process. The anchor holds the last handle of its job: when it dies the job is closed, which kills
// every member (KILL_ON_JOB_CLOSE) and removes the name.
func (w *fakeWin) endProc(pid int) {
	delete(w.procs, pid)
	for name, h := range w.holder {
		if h != pid {
			continue
		}
		for _, m := range w.members(name) {
			delete(w.procs, m)
		}
		delete(w.holder, name)
		delete(w.jobs, name)
	}
}
func (w *fakeWin) members(name string) []int {
	var out []int
	for pid, p := range w.procs {
		if p.job == name {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}
func (w *fakeWin) CreateJob(name string) (Job, error) {
	if w.failAt == "create" {
		return nil, errors.New("access denied")
	}
	if _, ok := w.jobs[name]; ok {
		return nil, errors.New("exists")
	}
	j := &fakeWinJob{w: w, name: name}
	w.jobs[name] = j
	return j, nil
}
func (w *fakeWin) OpenJob(name string) (Job, error) {
	if j, ok := w.jobs[name]; ok {
		return &fakeWinJob{w: w, name: j.name}, nil
	}
	if w.visible[name] > 0 {
		w.visible[name]--
		return &fakeWinJob{w: w, name: name}, nil
	}
	return nil, ErrJobAbsent
}
func (w *fakeWin) Adopt(j Job, pid int, stamp string) error {
	if w.failAt == "adopt" {
		return errors.New("assign refused")
	}
	p := w.procs[pid]
	if p == nil || p.stamp != stamp {
		return errors.New("not the anchor")
	}
	name := j.(*fakeWinJob).name
	p.job = name
	w.holder[name] = pid
	return nil
}
func (w *fakeWin) ProcInfo(pid int) (string, string, error) {
	p := w.procs[pid]
	if p == nil {
		return "", "", errors.New("no such process")
	}
	return p.stamp, p.comm, nil
}
func (w *fakeWin) ParentFor(cmd *exec.Cmd, pid int, stamp string) (func(), error) {
	if w.parentErr != nil {
		return nil, w.parentErr
	}
	if p := w.procs[pid]; p == nil || p.stamp != stamp {
		return nil, errors.New("anchor changed")
	}
	w.parentFor = append(w.parentFor, pid)
	return func() {}, nil
}
func (w *fakeWin) SelfTestChild(pid int, stamp string) (int, func(), error) {
	if w.parentErr != nil {
		return 0, nil, w.parentErr
	}
	a := w.procs[pid]
	if a == nil || a.stamp != stamp {
		return 0, nil, errors.New("anchor changed")
	}
	w.next++
	c := &fakeWinProc{stamp: fmt.Sprintf("s%d", w.next), comm: "ping.exe"}
	if !w.childOutside {
		c.job = a.job
	}
	w.procs[w.next] = c
	child := w.next
	return child, func() { delete(w.procs, child) }, nil
}

func (j *fakeWinJob) Pids() ([]int, error) {
	m := j.w.members(j.name)
	if _, live := j.w.jobs[j.name]; !live && j.w.visible[j.name] > 0 {
		j.w.visible[j.name]--
		m = append(m, 999999) // the closing handle's process is still listed for a moment
	}
	return m, nil
}
func (j *fakeWinJob) Close() error { return nil }
func (j *fakeWinJob) Contains(pid int) (bool, error) {
	p := j.w.procs[pid]
	return p != nil && p.job == j.name, nil
}
func (j *fakeWinJob) Terminate() error {
	if j.w.terminateErr != nil {
		return j.w.terminateErr
	}
	j.w.terminate++
	for _, pid := range j.w.members(j.name) {
		delete(j.w.procs, pid)
	}
	delete(j.w.holder, j.name)
	delete(j.w.jobs, j.name)
	if j.w.lingers > 0 {
		j.w.visible[j.name] = j.w.lingers
	}
	return nil
}
func (j *fakeWinJob) TerminateMember(pid int) error {
	if j.w.memberErr != nil {
		return j.w.memberErr
	}
	if ok, _ := j.Contains(pid); !ok {
		return errors.New("not a member")
	}
	j.w.endProc(pid)
	return nil
}

