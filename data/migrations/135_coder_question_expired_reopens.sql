-- 135_coder_question_expired_reopens.sql
--
-- A parked question's answer note can expire while the max_concurrent cap holds it. That is
-- not a failed answer, so it is counted apart from reopen_count; after a bound the question
-- stays parked for a person.
ALTER TABLE coder_session_questions ADD COLUMN expired_reopens INTEGER NOT NULL DEFAULT 0;
