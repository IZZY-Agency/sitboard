package proc

import "golang.org/x/sys/unix"

func readArgsEnv(pid int, pick Pick) bool {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return false
	}
	defer clear(buf) // the rest of the arguments and environment is not kept
	args, env, ok := ParseProcArgs2(buf)
	if !ok {
		return false
	}
	pick(args, env)
	return true
}
