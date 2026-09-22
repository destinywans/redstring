-- Query text is literal: LIKE metacharacters are never interpreted.
-- Chinese characters present in the query remain literal in candidate keys.
CREATE OR REPLACE FUNCTION aiops_search_keys(value text, preserve text)
RETURNS text[] LANGUAGE sql STABLE PARALLEL SAFE AS $$
 SELECT ARRAY[
  coalesce(string_agg(CASE WHEN strpos(preserve,c.ch)>0 THEN c.ch ELSE coalesce(d.pinyin,c.ch) END,'' ORDER BY c.ordinality),''),
  coalesce(string_agg(CASE WHEN strpos(preserve,c.ch)>0 THEN c.ch ELSE coalesce(left(d.pinyin,1),c.ch) END,'' ORDER BY c.ordinality),'')
 ]
 FROM regexp_split_to_table(regexp_replace(replace(lower(coalesce(value,'')),'ü','v'),'[[:space:]]','','g'),'') WITH ORDINALITY AS c(ch,ordinality)
 LEFT JOIN aiops_pinyin_dictionary d ON d.character=c.ch;
$$;

CREATE OR REPLACE FUNCTION aiops_search_match(value text, query text)
RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL SAFE AS $$
DECLARE
 candidate text := lower(coalesce(value,''));
 needle text := lower(regexp_replace(coalesce(query,''),'^[[:space:]]+|[[:space:]]+$','','g'));
 keys text[];
BEGIN
 IF strpos(candidate,needle)>0 THEN RETURN true; END IF;
 -- Pure Chinese searches must not match unrelated words sharing initials.
 IF needle !~ '[a-z]' THEN RETURN false; END IF;
 needle := regexp_replace(replace(needle,'ü','v'),'[[:space:]]','','g');
 candidate := regexp_replace(replace(candidate,'ü','v'),'[[:space:]]','','g');
 -- Common IDs and English-only columns do not need dictionary lookups.
 IF candidate !~ '[^ -~]' THEN RETURN strpos(candidate,needle)>0; END IF;
 keys := aiops_search_keys(candidate,needle);
 RETURN strpos(keys[1],needle)>0 OR strpos(keys[2],needle)>0;
END;
$$;
