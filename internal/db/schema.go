package db

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed ringq-schema.sql
var ringqSchema string

// RingQSchema is the DDL of the RingQ tables RingQswitch uses.
func RingQSchema() string { return ringqSchema }

var reCreate = regexp.MustCompile(`(?is)^\s*(?:--[^\n]*\n\s*)*create\s+(table|index)\s+(\w+)(?:\s+on\s+(\w+))?`)

// CreateMissingTables creates the RingQ tables RingQswitch uses
// that do not exist yet (a new database gets all of them; existing tables
// are never changed). It returns the names of the tables it created.
func (s *Store) CreateMissingTables(ctx context.Context) ([]string, error) {
	var created []string
	made := map[string]bool{}
	for _, stmt := range strings.Split(ringqSchema, ";\n") {
		m := reCreate.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		kind, name, on := strings.ToLower(m[1]), m[2], m[3]
		if kind == "table" && s.HasTable(name) || kind == "index" && !made[on] {
			continue
		}
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return created, fmt.Errorf("creating %s %s: %w", kind, name, err)
		}
		if kind == "table" {
			made[name] = true
			created = append(created, name)
		}
	}
	return created, s.loadSchema(ctx)
}

// RingQswitch's own tables, for features RingQ has no table for (or
// whose RingQ tables have unclear semantics). They are created when missing
// and never altered or dropped; RingQ tables are not touched.
var rqSchema = []string{
	`create table if not exists rq_aux_codes (
		aux_code_uuid uuid primary key,
		domain_uuid uuid,
		aux_code_name text not null,
		block_direct_calls boolean not null default false,
		aux_code_order integer not null default 0,
		aux_code_enabled boolean not null default true,
		insert_date timestamptz)`,
	`create table if not exists rq_agent_state (
		call_center_agent_uuid uuid primary key,
		domain_uuid uuid,
		agent_name text,
		extension text,
		status text,
		aux_code text,
		state text,
		status_since timestamptz,
		hostname text,
		update_date timestamptz)`,
	`create table if not exists rq_agent_status_log (
		log_uuid uuid primary key,
		domain_uuid uuid,
		call_center_agent_uuid uuid,
		agent_name text,
		extension text,
		status text,
		aux_code text,
		previous_status text,
		previous_aux_code text,
		previous_seconds integer,
		source text,
		hostname text,
		insert_date timestamptz)`,
	`create index if not exists rq_agent_status_log_agent_date on rq_agent_status_log (call_center_agent_uuid, insert_date)`,
	`create index if not exists rq_agent_status_log_domain_date on rq_agent_status_log (domain_uuid, insert_date)`,
	`create table if not exists rq_queue_calls (
		queue_call_uuid uuid primary key,
		domain_uuid uuid,
		call_center_queue_uuid uuid,
		queue_name text,
		queue_extension text,
		caller_id_name text,
		caller_id_number text,
		joined_at timestamptz,
		answered_at timestamptz,
		ended_at timestamptz,
		wait_seconds integer,
		talk_seconds integer,
		result text,
		call_center_agent_uuid uuid,
		agent_name text,
		agent_extension text,
		agents_offered integer,
		no_answer_count integer,
		hostname text)`,
	`create index if not exists rq_queue_calls_queue_date on rq_queue_calls (call_center_queue_uuid, joined_at)`,
	`create index if not exists rq_queue_calls_domain_date on rq_queue_calls (domain_uuid, joined_at)`,
	// the caller's call (its CDR and transcript)
	`alter table rq_queue_calls add column if not exists call_uuid uuid`,
	// Call transcription: one row per transcribed call (its A leg), and
	// each domain's settings.
	`create table if not exists rq_transcripts (
		call_uuid uuid primary key,
		domain_uuid uuid not null,
		status text not null,
		direction text,
		speaker_a text,
		speaker_b text,
		customer text,
		recording text,
		language text,
		duration real,
		stereo boolean,
		segments jsonb,
		speakers jsonb,
		sentiment real,
		sentiment_label text,
		customer_sentiment real,
		customer_label text,
		engine text,
		error text,
		created_at timestamptz not null default now(),
		updated_at timestamptz not null default now())`,
	`alter table rq_transcripts add column if not exists kind text not null default 'call'`,
	`create index if not exists rq_transcripts_domain on rq_transcripts (domain_uuid, created_at)`,
	`create index if not exists rq_transcripts_status on rq_transcripts (status)`,
	`create table if not exists rq_transcription_settings (
		domain_uuid uuid primary key,
		enabled boolean not null default false,
		scope text not null default 'external',
		language text not null default 'auto',
		updated_at timestamptz)`,
	`alter table rq_transcription_settings add column if not exists model text not null default ''`,
	`alter table rq_transcription_settings add column if not exists voicemail boolean not null default true`,
	`alter table rq_transcription_settings add column if not exists live boolean not null default false`,
	`alter table rq_transcription_settings add column if not exists live_model text not null default ''`,
	`create table if not exists rq_outbound_routes (
		outbound_route_uuid uuid primary key,
		domain_uuid uuid,
		dialplan_uuid uuid,
		route_name text,
		route_pattern text,
		route_strip integer not null default 0,
		route_prepend text,
		gateway_uuids text,
		caller_id_number text,
		caller_id_name text,
		route_order integer not null default 100,
		route_enabled boolean not null default true,
		route_description text,
		insert_date timestamptz,
		update_date timestamptz)`,
	// Outbound rules (RingQ style): matching by caller, prefix and length,
	// BLOCK, and how the call presents itself to the carrier.
	`alter table rq_outbound_routes add column if not exists route_prefix text`,
	`alter table rq_outbound_routes add column if not exists route_length text`,
	`alter table rq_outbound_routes add column if not exists route_from_extensions text`,
	`alter table rq_outbound_routes add column if not exists route_from_ranges text`,
	`alter table rq_outbound_routes add column if not exists route_block boolean not null default false`,
	`alter table rq_outbound_routes add column if not exists sip_from_user text`,
	`alter table rq_outbound_routes add column if not exists sip_from_display text`,
	`alter table rq_outbound_routes add column if not exists sip_from_host text`,
	`alter table rq_outbound_routes add column if not exists cid_type text`,
	`create table if not exists rq_time_conditions (
		time_condition_uuid uuid primary key,
		domain_uuid uuid,
		dialplan_uuid uuid,
		condition_name text,
		condition_extension text,
		weekdays text,
		time_from text,
		time_to text,
		holidays text,
		match_action text,
		nomatch_action text,
		condition_enabled boolean not null default true,
		condition_description text,
		insert_date timestamptz,
		update_date timestamptz)`,
	// Web portal access of extensions (separate from the SIP password).
	`create table if not exists rq_web_users (
		extension_uuid uuid primary key,
		domain_uuid uuid,
		role text not null default 'user',
		web_password text,
		enabled boolean not null default true,
		last_login timestamptz,
		insert_date timestamptz,
		update_date timestamptz)`,
	// Shared address book (Contacts).
	`create table if not exists rq_contacts (
		contact_uuid uuid primary key,
		domain_uuid uuid not null,
		name text not null,
		company text,
		phone text,
		mobile text,
		email text,
		notes text,
		created_by text,
		insert_date timestamptz,
		update_date timestamptz)`,
	`create index if not exists rq_contacts_domain on rq_contacts (domain_uuid)`,
	// Internal chat.
	`create table if not exists rq_chat_rooms (
		room_uuid uuid primary key,
		domain_uuid uuid,
		room_type text not null default 'group',
		room_name text,
		direct_key text,
		created_by text,
		insert_date timestamptz)`,
	`create index if not exists rq_chat_rooms_direct on rq_chat_rooms (domain_uuid, direct_key)`,
	`create table if not exists rq_chat_members (
		room_uuid uuid not null,
		member text not null,
		last_read timestamptz,
		primary key (room_uuid, member))`,
	`create table if not exists rq_chat_messages (
		message_uuid uuid primary key,
		room_uuid uuid not null,
		domain_uuid uuid,
		sender text,
		sender_name text,
		body text,
		file_name text,
		file_size bigint,
		file_type text,
		insert_date timestamptz)`,
	`create index if not exists rq_chat_messages_room_date on rq_chat_messages (room_uuid, insert_date)`,
	// Web meetings.
	`create table if not exists rq_meetings (
		meeting_uuid uuid primary key,
		domain_uuid uuid,
		meeting_code text unique,
		title text,
		passcode text,
		host text,
		host_name text,
		scheduled_at timestamptz,
		duration_minutes integer,
		lobby boolean not null default true,
		allow_guests boolean not null default true,
		description text,
		insert_date timestamptz)`,
	// Meetings started with "Start meeting" (not planned ones).
	`alter table rq_meetings add column if not exists instant boolean not null default false`,
	`alter table rq_meetings alter column lobby set default true`,
	// The web password, encrypted with a key kept on the server, so owners
	// can see it on the extension page (the hash is what sign-in checks).
	`alter table rq_web_users add column if not exists web_password_enc text`,
	// Single sign-on: each domain's identity providers (Microsoft, Google,
	// LinkedIn; the client secret encrypted with the data-dir key), its
	// sign-in options, and the email that signs an extension in.
	`create table if not exists rq_sso_providers (
		domain_uuid uuid not null,
		provider text not null,
		client_id text,
		client_secret_enc text,
		tenant_id text,
		enabled boolean not null default false,
		insert_date timestamptz default now(),
		update_date timestamptz,
		primary key (domain_uuid, provider))`,
	`create table if not exists rq_signin_options (
		domain_uuid uuid primary key,
		web_password boolean not null default true,
		update_date timestamptz)`,
	`alter table rq_web_users add column if not exists sso_email text`,
	// Hotel front desk: guests checked in to rooms (extensions) and their
	// wake-up calls.
	`create table if not exists rq_hotel_stays (
		stay_uuid uuid primary key,
		domain_uuid uuid not null,
		extension text not null,
		first_name text,
		last_name text,
		checked_in_at timestamptz not null default now(),
		checked_in_by text,
		checked_out_at timestamptz,
		checked_out_by text)`,
	`create index if not exists rq_hotel_stays_open on rq_hotel_stays (domain_uuid, extension) where checked_out_at is null`,
	`create table if not exists rq_wakeup_calls (
		wakeup_uuid uuid primary key,
		domain_uuid uuid not null,
		extension text not null,
		stay_uuid uuid,
		call_at timestamptz not null,
		sound text,
		status text not null default 'scheduled',
		attempts integer not null default 0,
		last_result text,
		created_by text,
		insert_date timestamptz default now(),
		update_date timestamptz)`,
	`create index if not exists rq_wakeup_calls_due on rq_wakeup_calls (status, call_at)`,
	// What a finished meeting leaves behind: who came, the chat and the
	// transcript (Recent meeting content). members are the signed-in
	// participants, who may read it.
	`create table if not exists rq_meeting_history (
		history_uuid uuid primary key,
		domain_uuid uuid,
		meeting_code text,
		title text,
		host text,
		started timestamptz,
		ended timestamptz,
		participants jsonb,
		members text[],
		chat jsonb,
		transcript jsonb,
		files jsonb)`,
	`create index if not exists rq_meeting_history_domain on rq_meeting_history (domain_uuid, ended desc)`,
	// Security rules: ip-deny, ip-allow (never auto-block), web-allow
	// (only these addresses may open the web interface), ua-deny.
	// Fax extensions: the RingQ portal's v_fax (same columns), created here so a
	// RingQ database without the RingQ portal's fax app gets it on start.
	`create table if not exists v_fax (
		fax_uuid uuid primary key, domain_uuid uuid, dialplan_uuid uuid, fax_extension text,
		fax_destination_number text, fax_prefix text, fax_name text, fax_email text, fax_file text,
		fax_email_connection_type text, fax_email_connection_host text, fax_email_connection_port text,
		fax_email_connection_security text, fax_email_connection_validate text, fax_email_connection_username text,
		fax_email_connection_password text, fax_email_connection_mailbox text, fax_email_inbound_subject_tag text,
		fax_email_outbound_subject_tag text, fax_email_outbound_authorized_senders text, fax_pin_number text,
		fax_caller_id_name text, fax_caller_id_number text, fax_toll_allow text, fax_forward_number text,
		fax_send_channels numeric, fax_description text, accountcode text,
		insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid, fax_send_greeting text)`,
	// eFax: the RingQ fax engine's own tables (same columns, so it and
	// RingQswitch can share them): every fax sent or received, and each
	// domain's station id and page header.
	`create table if not exists fax_jobs (
		id uuid primary key,
		domain_uuid uuid not null,
		direction text not null,
		from_number text,
		to_number text,
		pages integer default 0,
		status text not null,
		file_path text,
		call_uuid text,
		esl_job_uuid text,
		error_reason text,
		created_at double precision not null,
		updated_at double precision not null)`,
	`create index if not exists idx_fax_jobs_domain on fax_jobs (domain_uuid)`,
	`create index if not exists idx_fax_jobs_call_uuid on fax_jobs (call_uuid)`,
	`create index if not exists idx_fax_jobs_status on fax_jobs (direction, status)`,
	`create table if not exists fax_domain_settings (
		domain_uuid uuid primary key,
		fax_ident text,
		fax_header text,
		updated_at double precision not null)`,
	`create table if not exists rq_security_rules (
		rule_uuid uuid primary key,
		rule_type text not null,
		rule_value text not null,
		description text,
		enabled boolean not null default true,
		insert_date timestamptz)`,
	// RingQ's phone keys (busy lamp fields) per extension and per company,
	// as in the RingQ database: created only where they are missing. types
	// and values are JSON lists, one entry per key.
	`create table if not exists q_blfs (
		domain_uuid uuid not null,
		user_uuid uuid not null primary key,
		types text,
		"values" text,
		insert_date timestamptz,
		update_date timestamptz,
		extension text,
		insert_uuid uuid,
		update_uuid uuid)`,
	`create table if not exists q_company_blfs (
		id serial primary key,
		domain_uuid uuid not null,
		types text,
		"values" text,
		insert_date timestamptz,
		update_date timestamptz,
		insert_uuid uuid,
		update_uuid uuid,
		show_dialer smallint not null default 1,
		show_navigation smallint not null default 1)`,
	// the RingQ portal's domain settings (time zone, language) and RingQ's office
	// hours, holidays and security settings, created only where missing,
	// as in the RingQ database.
	`create table if not exists v_domain_settings (
		domain_uuid uuid,
		domain_setting_uuid uuid primary key,
		app_uuid uuid,
		domain_setting_category text,
		domain_setting_subcategory text,
		domain_setting_name text,
		domain_setting_value text,
		domain_setting_order numeric,
		domain_setting_enabled boolean,
		domain_setting_description text,
		insert_date timestamptz,
		insert_user uuid,
		update_date timestamptz,
		update_user uuid)`,
	`create table if not exists q_time_schedules (
		domain_uuid uuid not null,
		work_default text default '',
		work_schedule text default '1-1081-540,2-1081-540,3-1081-540,4-1081-540,5-1081-540,6-1081-540,7-1081-540',
		break_default text default '',
		break_schedule text default '1-720-781,2-720-781,3-720-781,4-720-781,5-720-781,6-720-811,7-720-781',
		special_schedule text,
		insert_uuid uuid,
		insert_date timestamptz not null,
		updated_uuid uuid,
		update_date timestamptz not null,
		id serial primary key,
		work_extensions text,
		break_extensions text,
		special_extensions text,
		special_default text,
		special_filename text)`,
	`create table if not exists q_holidays (
		id serial primary key,
		domain_uuid uuid not null,
		holiday_name text,
		holiday_greeting text,
		month smallint,
		day smallint,
		year smallint,
		recurring smallint,
		insert_date timestamptz,
		insert_user uuid,
		update_date timestamptz,
		update_user uuid,
		extensions text,
		type smallint,
		day2 smallint,
		month2 smallint,
		year2 smallint,
		time text,
		exemptions text,
		holiday_uuid uuid)`,
	`create table if not exists q_holiday_exemptions (
		exempt_uuid uuid not null,
		domain_uuid uuid,
		extension text,
		update_date timestamptz,
		update_user uuid,
		insert_date timestamptz,
		insert_user uuid,
		id serial primary key)`,
	`create table if not exists q_security_restrictions (
		domain_uuid uuid not null,
		security_uuid uuid not null,
		blocked_countries text default '' not null,
		blocked_ips text default '',
		id serial primary key,
		allowed_ips text default '',
		blocked_descriptions text,
		allowed_descriptions text)`,
	`create table if not exists q_security_rules (
		domain_uuid uuid not null,
		security_uuid uuid not null,
		pin_all_outbound text default '' not null,
		prefixes text default '00',
		trusted_device text,
		countries text,
		id serial primary key,
		pin_no_outbound text default '' not null,
		pin_prefix_outbound text default '' not null)`,
	`create table if not exists v_pin_numbers (
		domain_uuid uuid,
		pin_number_uuid uuid primary key,
		pin_number text,
		accountcode text,
		enabled text,
		description text,
		insert_date timestamptz,
		insert_user uuid,
		update_date timestamptz,
		update_user uuid)`,
	// SIP Guard's bans (one row per address and protection) and its
	// history, as RingQ keeps them.
	`create table if not exists sip_security_blocks (
		id bigserial primary key,
		ip_address inet not null,
		category varchar(32) not null,
		reason text not null,
		failure_count integer default 0 not null,
		first_seen_at timestamptz not null,
		last_seen_at timestamptz not null,
		blocked_at timestamptz not null,
		unblocked_at timestamptz,
		status varchar(16) not null,
		source varchar(16) default 'local' not null,
		jail_name varchar(64),
		metadata jsonb,
		constraint sip_security_blocks_ip_category_uniq unique (ip_address, category))`,
	`create table if not exists security_audit_log (
		id bigserial primary key,
		occurred_at timestamptz default now() not null,
		actor varchar(64),
		action varchar(32) not null,
		ip_address inet not null,
		category varchar(32),
		reason text,
		metadata jsonb)`,
	`create index if not exists idx_audit_time on security_audit_log (occurred_at)`,
	// Server and domain settings of RingQswitch (scope: "server" or a
	// domain uuid): SIP Guard, global IP restriction, web auto logout.
	`create table if not exists rq_settings (
		scope text not null,
		name text not null,
		value text not null default '',
		updated_at timestamptz,
		primary key (scope, name))`,
	// AI agents: a number that answers calls with a voice AI. Greetings are
	// a JSON list of strings; knowledge_urls one web address per line.
	`create table if not exists rq_ai_agents (
		ai_agent_uuid uuid primary key,
		domain_uuid uuid,
		dialplan_uuid uuid,
		agent_name text,
		agent_extension text,
		agent_enabled boolean not null default true,
		agent_voice text,
		agent_noise text,
		agent_languages text,
		agent_greetings text,
		agent_prompt text,
		agent_context text,
		knowledge_urls text,
		knowledge_text text,
		max_minutes integer not null default 10,
		allow_end_call boolean not null default true,
		fallback_action text,
		agent_description text,
		insert_date timestamptz,
		update_date timestamptz)`,
	// Connect keywords: what the caller asks for (or says) and where the
	// call goes then (a destination action, "transfer:1000 XML <domain>").
	`create table if not exists rq_ai_agent_keywords (
		ai_agent_keyword_uuid uuid primary key,
		ai_agent_uuid uuid,
		domain_uuid uuid,
		keyword text,
		destination text,
		keyword_order integer not null default 10,
		insert_date timestamptz)`,
	`create index if not exists rq_ai_agent_keywords_agent on rq_ai_agent_keywords (ai_agent_uuid)`,
	// What the agent knows: the agent's web pages and FAQ text, in chunks.
	`create table if not exists rq_ai_knowledge (
		ai_knowledge_uuid uuid primary key,
		ai_agent_uuid uuid,
		domain_uuid uuid,
		source text,
		title text,
		chunk_no integer not null default 0,
		content text,
		fetched_at timestamptz)`,
	`create index if not exists rq_ai_knowledge_agent on rq_ai_knowledge (ai_agent_uuid)`,
	// Calls the AI agents took, with what was said (a JSON list of turns).
	`create table if not exists rq_ai_calls (
		ai_call_uuid uuid primary key,
		domain_uuid uuid,
		ai_agent_uuid uuid,
		call_uuid text,
		caller_number text,
		caller_name text,
		destination_number text,
		started_at timestamptz,
		ended_at timestamptz,
		duration integer not null default 0,
		outcome text,
		transferred_to text,
		turns integer not null default 0,
		transcript text,
		error text)`,
	`create index if not exists rq_ai_calls_agent_date on rq_ai_calls (ai_agent_uuid, started_at)`,
	`create index if not exists rq_ai_calls_domain_date on rq_ai_calls (domain_uuid, started_at)`,
	// The voice AI service of a domain; the API key is encrypted with the
	// server's key (data-dir/web-passwords.key), not readable from the DB.
	`create table if not exists rq_ai_settings (
		domain_uuid uuid primary key,
		api_key_enc text,
		model text not null default '',
		base_url text not null default '',
		updated_at timestamptz)`,
}

// EnsureSchema creates RingQswitch's own tables when they are missing and
// refreshes the cached schema. Failures (e.g. no CREATE permission) are
// returned; features using those tables are then unavailable.
func (s *Store) EnsureSchema(ctx context.Context) error {
	var errs []string
	for _, stmt := range rqSchema {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := s.loadSchema(ctx); err != nil {
		return err
	}
	if len(errs) > 0 {
		return &schemaError{errs}
	}
	return nil
}

type schemaError struct{ errs []string }

func (e *schemaError) Error() string {
	return "creating RingQswitch tables: " + strings.Join(e.errs, "; ")
}
