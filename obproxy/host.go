package obproxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// initializeOBProxy keeps the vendor's process-wide signal handlers outside Go.
func initializeOBProxy(spec obProxySpec) error {
	cmd := exec.Command(spec.host, spec.library)
	cmd.Dir = spec.directory
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, writer, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	// Own the read end: cmd.Wait must not close unread readiness bytes.
	cmd.Stdout = writer
	if err = cmd.Start(); err != nil {
		_ = writer.Close()
		_ = input.Close()
		_ = output.Close()
		return err
	}
	_ = writer.Close()
	// Keep stdin alive after readiness; the native host exits on parent EOF.
	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
		_ = input.Close()
	}()
	failed := true
	defer func() {
		_ = output.Close()
		if failed {
			_ = input.Close()
			_ = cmd.Process.Kill()
			<-wait
		}
	}()
	data := binary.LittleEndian.AppendUint32(nil, uint32(len(spec.config)))
	data = append(data, spec.config...)
	if _, err = io.Copy(input, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("oceanbase: OBProxy host configuration: %w", err)
	}
	var ready [8]byte
	if _, err = io.ReadFull(output, ready[:]); err != nil {
		return fmt.Errorf("oceanbase: OBProxy host readiness: %w", err)
	}
	if string(ready[:4]) != "OBV1" {
		return fmt.Errorf("oceanbase: invalid OBProxy host readiness")
	}
	if code := int32(binary.LittleEndian.Uint32(ready[4:])); code != 0 {
		return fmt.Errorf("oceanbase: OBProxy host status %d", code)
	}
	failed = false
	return nil
}
