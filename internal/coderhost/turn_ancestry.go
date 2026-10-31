package coderhost

// CallerInATurn says whether the calling process is the process of a running coder turn, or one of
// its descendants, whatever its environment holds. It reuses the ancestry check that binds a session
// token to its turn (descendsFrom); it makes no new binding. It is a courtesy layer of the same class
// as that check: it reads the process tree, so a process that detaches from it is not seen. Where
// ancestry cannot be read it says false.
func (s *Store) CallerInATurn() (bool, error) {
	if !ancestrySupported {
		return false, nil
	}
	rows, err := s.db.Query(`SELECT turn_pid, turn_stamp FROM coder_sessions WHERE turn_pid > 1`)
	if err != nil {
		return false, err
	}
	type turn struct {
		pid   int
		stamp string
	}
	var turns []turn
	for rows.Next() {
		var t turn
		if err := rows.Scan(&t.pid, &t.stamp); err != nil {
			rows.Close()
			return false, err
		}
		turns = append(turns, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, t := range turns {
		if descendsFrom(t.pid, t.stamp) {
			return true, nil
		}
	}
	return false, nil
}
