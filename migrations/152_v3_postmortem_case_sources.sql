-- Keep existing ops postmortems bound to their real investigation.close event.
-- Manual case postmortems instead bind a real case status transition; a case
-- may legitimately have no investigation because open_canvas was false.
ALTER TABLE v3_postmortems
 ALTER COLUMN investigation_id DROP NOT NULL,
 ALTER COLUMN close_event_seq DROP NOT NULL,
 ADD COLUMN source_kind text NOT NULL DEFAULT 'ops'
   CHECK (source_kind IN ('ops','case')),
 ADD COLUMN source_case_status_event_id bigint,
 ADD CONSTRAINT v3_postmortems_exact_source
   CHECK ((source_kind='ops' AND investigation_id IS NOT NULL AND
           close_event_seq IS NOT NULL AND source_case_status_event_id IS NULL) OR
          (source_kind='case' AND close_event_seq IS NULL AND
           source_case_status_event_id IS NOT NULL)),
 ADD CONSTRAINT v3_postmortems_case_event_same_case
   FOREIGN KEY (incident_id, source_case_status_event_id)
   REFERENCES v3_incident_case_status_events(case_id, id);
