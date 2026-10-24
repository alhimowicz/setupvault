package vault

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type recoveryCheckpoint struct {
	File    *RecoveryFile `json:"file,omitempty"`
	Key     *RecoveryKey  `json:"key,omitempty"`
	Phase   string        `json:"phase"`
	Updated time.Time     `json:"updated"`
}

func (a *App) checkpointRecovery(j *RecoveryJob, file *RecoveryFile, key *RecoveryKey) error {
	dir, _ := a.recoveryDir(j.ID)
	data, err := json.Marshal(recoveryCheckpoint{File: file, Key: key, Phase: j.Phase, Updated: time.Now()})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile("checkpoints.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	j.checkpointCount++
	if j.checkpointCount >= 100 || time.Since(j.checkpointTime) >= time.Second {
		return a.saveRecovery(j)
	}
	return nil
}

func (a *App) readRecoveryCheckpoints(j *RecoveryJob, dir string) error {
	f, err := os.Open(filepath.Join(dir, "checkpoints.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 64<<20 {
		return errors.New("recovery checkpoint journal too large")
	}
	files := map[string]int{}
	keys := map[string]int{}
	for i, f := range j.Files {
		files[f.Path] = i
	}
	for i, k := range j.Keys {
		keys[k.Path] = i
	}
	scanner := bufio.NewScanner(io.LimitReader(f, 64<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var c recoveryCheckpoint
		if json.Unmarshal(scanner.Bytes(), &c) != nil {
			continue
		}
		if c.Updated.Before(j.Updated) {
			continue
		}
		if c.File != nil {
			if i, ok := files[c.File.Path]; ok {
				j.Files[i] = *c.File
			}
		}
		if c.Key != nil {
			if i, ok := keys[c.Key.Path]; ok {
				j.Keys[i] = *c.Key
			}
		}
		j.Phase = c.Phase
		j.Updated = c.Updated
	}
	return scanner.Err()
}
