-- The subset of the RingQ database schema (including the live state tables)
-- that RingQswitch reads and writes, with the column types RingQ uses.
-- Built into the binary: "ringqswitch -init-db" creates the tables that are
-- missing (all of them in a new database, none in a complete RingQ database).
-- Also used by the database integration tests.
create table v_domains (
	domain_uuid uuid primary key, domain_parent_uuid uuid, domain_name text,
	domain_enabled boolean, domain_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_extensions (
	extension_uuid uuid primary key, domain_uuid uuid, extension text, number_alias text, password text,
	accountcode text, effective_caller_id_name text, effective_caller_id_number text,
	outbound_caller_id_name text, outbound_caller_id_number text, emergency_caller_id_name text,
	emergency_caller_id_number text, directory_first_name text, directory_last_name text,
	directory_visible text, directory_exten_visible text, max_registrations text, limit_max text,
	limit_destination text, missed_call_app text, missed_call_data text, user_context text, toll_allow text,
	call_timeout numeric, call_group text, call_screen_enabled text, user_record text, hold_music text,
	auth_acl text, cidr text, sip_force_contact text, nibble_account text, sip_force_expires numeric,
	mwi_account text, sip_bypass_media text, unique_id numeric, dial_string text, dial_user text,
	dial_domain text, do_not_disturb text, forward_all_destination text, forward_all_enabled text,
	forward_busy_destination text, forward_busy_enabled text, forward_no_answer_destination text,
	forward_no_answer_enabled text, forward_user_not_registered_destination text,
	forward_user_not_registered_enabled text, follow_me_uuid uuid, follow_me_enabled text,
	follow_me_destinations text, extension_language text, extension_dialect text, extension_voice text,
	extension_type text, enabled text, description text, absolute_codec_string text, force_ping text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_gateways (
	gateway_uuid uuid primary key, domain_uuid uuid, gateway text, username text, password text,
	distinct_to text, auth_username text, realm text, from_user text, from_domain text, proxy text,
	register_proxy text, outbound_proxy text, expire_seconds numeric, register text,
	register_transport text, contact_params text, retry_seconds numeric, extension text, ping text,
	ping_min text, ping_max text, contact_in_ping text, caller_id_in_from text, supress_cng text,
	sip_cid_type text, codec_prefs text, channels numeric, extension_in_contact text, context text,
	profile text, hostname text, enabled text, description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_dialplans (
	domain_uuid uuid, dialplan_uuid uuid primary key, app_uuid uuid, hostname text, dialplan_context text,
	dialplan_name text, dialplan_number text, dialplan_destination text, dialplan_continue text,
	dialplan_xml text, dialplan_order numeric, dialplan_enabled text, dialplan_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_vars (
	var_uuid uuid primary key, var_category text, var_name text, var_value text, var_command text,
	var_hostname text, var_enabled text, var_order numeric, var_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_ring_groups (
	domain_uuid uuid, ring_group_uuid uuid primary key, ring_group_name text, ring_group_extension text,
	ring_group_greeting text, ring_group_context text, ring_group_call_timeout numeric,
	ring_group_forward_destination text, ring_group_forward_enabled text, ring_group_caller_id_name text,
	ring_group_caller_id_number text, ring_group_cid_name_prefix text, ring_group_cid_number_prefix text,
	ring_group_strategy text, ring_group_timeout_app text, ring_group_timeout_data text,
	ring_group_distinctive_ring text, ring_group_ringback text, ring_group_call_screen_enabled text,
	ring_group_call_forward_enabled text, ring_group_follow_me_enabled text, ring_group_missed_call_app text,
	ring_group_missed_call_data text, ring_group_enabled text, ring_group_description text, dialplan_uuid uuid,
	ring_group_forward_toll_allow text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_ring_group_destinations (
	ring_group_destination_uuid uuid primary key, domain_uuid uuid, ring_group_uuid uuid,
	destination_number text, destination_delay numeric, destination_timeout numeric,
	destination_enabled boolean, destination_prompt numeric,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid,
	destination_description text);
create table v_ivr_menus (
	ivr_menu_uuid uuid primary key, domain_uuid uuid, dialplan_uuid uuid, ivr_menu_name text,
	ivr_menu_extension text, ivr_menu_parent_uuid uuid, ivr_menu_language text, ivr_menu_dialect text,
	ivr_menu_voice text, ivr_menu_greet_long text, ivr_menu_greet_short text, ivr_menu_invalid_sound text,
	ivr_menu_exit_sound text, ivr_menu_pin_number text, ivr_menu_confirm_macro text,
	ivr_menu_confirm_key text, ivr_menu_tts_engine text, ivr_menu_tts_voice text,
	ivr_menu_confirm_attempts numeric, ivr_menu_timeout numeric, ivr_menu_exit_app text,
	ivr_menu_exit_data text, ivr_menu_inter_digit_timeout numeric, ivr_menu_max_failures numeric,
	ivr_menu_max_timeouts numeric, ivr_menu_digit_len numeric, ivr_menu_direct_dial text,
	ivr_menu_ringback text, ivr_menu_cid_prefix text, ivr_menu_context text, ivr_menu_enabled text,
	ivr_menu_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_ivr_menu_options (
	ivr_menu_option_uuid uuid primary key, ivr_menu_uuid uuid, domain_uuid uuid, ivr_menu_option_digits text,
	ivr_menu_option_action text, ivr_menu_option_param text, ivr_menu_option_order numeric,
	ivr_menu_option_description text, ivr_menu_option_enabled boolean,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_access_controls (
	access_control_uuid uuid primary key, access_control_name text, access_control_default text,
	access_control_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_access_control_nodes (
	access_control_node_uuid uuid primary key, access_control_uuid uuid, node_type text, node_cidr text,
	node_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table registrations (
	reg_user varchar(256), realm varchar(256), token varchar(256), url text, expires integer,
	network_ip varchar(256), network_port varchar(256), network_proto varchar(256),
	hostname varchar(256), metadata varchar(256));
create index regindex1 on registrations (reg_user, realm, hostname);
create table channels (
	uuid varchar(256), direction varchar(32), created varchar(128), created_epoch integer,
	name varchar(1024), state varchar(64), cid_name varchar(1024), cid_num varchar(256),
	ip_addr varchar(256), dest varchar(1024), application varchar(128), application_data varchar(4096),
	dialplan varchar(128), context varchar(128), read_codec varchar(128), read_rate varchar(32),
	read_bit_rate varchar(32), write_codec varchar(128), write_rate varchar(32), write_bit_rate varchar(32),
	secure varchar(64), hostname varchar(256), presence_id varchar(4096), presence_data varchar(4096),
	accountcode varchar(256), callstate varchar(64), callee_name varchar(1024), callee_num varchar(256),
	callee_direction varchar(5), call_uuid varchar(256), sent_callee_name varchar(1024),
	sent_callee_num varchar(256), initial_cid_name varchar(1024), initial_cid_num varchar(256),
	initial_ip_addr varchar(256), initial_dest varchar(1024), initial_dialplan varchar(128),
	initial_context varchar(128));
create table calls (
	call_uuid varchar(255), call_created varchar(128), call_created_epoch integer,
	caller_uuid varchar(256), callee_uuid varchar(256), hostname varchar(256));
create table v_xml_cdr (
	xml_cdr_uuid uuid primary key, domain_uuid uuid, provider_uuid uuid, extension_uuid uuid,
	sip_call_id text, domain_name text, accountcode text, direction text, default_language text,
	context text, caller_id_name text, caller_id_number text, caller_destination text,
	source_number text, destination_number text, start_epoch numeric, start_stamp timestamptz,
	answer_stamp timestamptz, answer_epoch numeric, end_epoch numeric, end_stamp timestamptz,
	duration numeric, mduration numeric, billsec numeric, billmsec numeric, hold_accum_seconds numeric,
	bridge_uuid text, read_codec text, read_rate text, write_codec text, write_rate text,
	remote_media_ip text, network_addr text, record_path text, record_name text, record_length numeric,
	record_transcription text, leg character(1), originating_leg_uuid uuid, pdd_ms numeric,
	rtp_audio_in_mos numeric, last_app text, last_arg text, voicemail_message boolean,
	missed_call boolean, call_center_queue_uuid uuid, cc_side text, cc_member_uuid uuid,
	cc_queue_joined_epoch numeric, cc_queue text, cc_member_session_uuid uuid, cc_agent_uuid uuid,
	cc_agent text, cc_agent_type text, cc_agent_bridged text, cc_queue_answered_epoch numeric,
	cc_queue_terminated_epoch numeric, cc_queue_canceled_epoch numeric, cc_cancel_reason text,
	cc_cause text, waitsec numeric, conference_name text, conference_uuid uuid,
	conference_member_id text, digits_dialed text, pin_number text, status text,
	call_disposition text, hangup_cause text, hangup_cause_q850 numeric,
	sip_hangup_disposition text, ring_group_uuid uuid, ivr_menu_uuid uuid, call_flow jsonb,
	xml text, json jsonb,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_destinations (
	domain_uuid uuid, destination_uuid uuid primary key, dialplan_uuid uuid, fax_uuid uuid, user_uuid uuid,
	group_uuid uuid, destination_type text, destination_number text, destination_trunk_prefix text,
	destination_area_code text, destination_prefix text, destination_condition_field text,
	destination_number_regex text, destination_caller_id_name text, destination_caller_id_number text,
	destination_cid_name_prefix text, destination_context text, destination_record text,
	destination_hold_music text, destination_distinctive_ring text, destination_accountcode text,
	destination_type_voice numeric, destination_type_fax numeric, destination_type_emergency numeric,
	destination_type_text numeric, destination_conditions json, destination_actions json,
	destination_app text, destination_data text, destination_alternate_app text,
	destination_alternate_data text, destination_order numeric, destination_enabled text,
	destination_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_call_center_queues (
	call_center_queue_uuid uuid primary key, domain_uuid uuid, dialplan_uuid uuid, queue_name text,
	queue_extension text, queue_greeting text, queue_strategy text, queue_moh_sound text,
	queue_record_template text, queue_time_base_score text, queue_time_base_score_sec numeric,
	queue_max_wait_time numeric, queue_max_wait_time_with_no_agent numeric,
	queue_max_wait_time_with_no_agent_time_reached numeric, queue_tier_rules_apply text,
	queue_tier_rule_wait_second numeric, queue_tier_rule_no_agent_no_wait text, queue_timeout_action text,
	queue_discard_abandoned_after numeric, queue_abandoned_resume_allowed text,
	queue_tier_rule_wait_multiply_level text, queue_cid_prefix text, queue_outbound_caller_id_name text,
	queue_outbound_caller_id_number text, queue_announce_position text, queue_announce_sound text,
	queue_announce_frequency numeric, queue_cc_exit_keys text, queue_email_address text,
	queue_description text, queue_max_callers integer,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_call_center_agents (
	call_center_agent_uuid uuid primary key, domain_uuid uuid, user_uuid uuid, agent_name text,
	agent_type text, agent_call_timeout numeric, agent_id text, agent_password text, agent_contact text,
	agent_status text, agent_logout text, agent_max_no_answer numeric, agent_wrap_up_time numeric,
	agent_reject_delay_time numeric, agent_busy_delay_time numeric, agent_no_answer_delay_time text,
	agent_record text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_call_center_tiers (
	call_center_tier_uuid uuid primary key, domain_uuid uuid, call_center_queue_uuid uuid,
	call_center_agent_uuid uuid, agent_name text, queue_name text, tier_level numeric, tier_position numeric,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
-- The call centre's runtime tables
create table agents (
	name text, instance_id text, uuid text, type text, contact text, status text, state text,
	max_no_answer integer, wrap_up_time integer, reject_delay_time integer, busy_delay_time integer,
	no_answer_delay_time integer, last_bridge_start integer, last_bridge_end integer,
	last_offered_call integer, last_status_change integer, no_answer_count integer, calls_answered integer,
	talk_time integer, ready_time integer, external_calls_count integer);
create table tiers (queue text, agent text, state text, level integer, position integer);
create table members (
	queue text, instance_id text, uuid text, session_uuid text, cid_number text, cid_name text,
	system_epoch integer, joined_epoch integer, rejoined_epoch integer, bridge_epoch integer,
	abandoned_epoch integer, base_score integer, skill_score integer, serving_agent text,
	serving_system text, state text);
create table v_voicemails (
	domain_uuid uuid, voicemail_uuid uuid primary key, voicemail_id text, voicemail_password text,
	greeting_id numeric, voicemail_alternate_greet_id numeric, voicemail_recording_instructions text,
	voicemail_recording_options text, voicemail_mail_to text, voicemail_sms_to text,
	voicemail_transcription_enabled text, voicemail_attach_file text, voicemail_file text,
	voicemail_local_after_email text, voicemail_local_after_forward text, voicemail_enabled text,
	voicemail_description text, voicemail_name_base64 text, voicemail_tutorial text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_voicemail_messages (
	domain_uuid uuid, voicemail_message_uuid uuid primary key, voicemail_uuid uuid, created_epoch numeric,
	read_epoch numeric, caller_id_name text, caller_id_number text, message_length numeric,
	message_status text, message_priority text, message_intro_base64 text, message_base64 text,
	message_transcription text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_conferences (
	domain_uuid uuid, conference_uuid uuid primary key, dialplan_uuid uuid, conference_name text,
	conference_extension text, conference_pin_number text, conference_profile text,
	conference_email_address text, conference_account_code text, conference_flags text,
	conference_order numeric, conference_description text, conference_enabled text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
-- RingQ's AUX history and queue call log
create table q_auxcodes_list (
	id serial primary key, auxcode_uuid uuid, domain_uuid uuid, auxcode_status text, insert_date timestamptz,
	user_uuid uuid, extension text, username text);
create table q_call_queues_list (
	id serial primary key, domain_uuid uuid, queue_uuid uuid, call_uuid uuid, caller_id_number text,
	caller_id_name text, qbc_call text, callee_id_number text, callee_id_name text, call_direction text,
	call_type text, call_duration integer, call_start_time integer, call_answered_time integer,
	call_waiting_time integer, call_end_time integer, call_time_to_answer integer, wrapup_uuid uuid,
	update_date timestamptz, update_user uuid, insert_date timestamptz, insert_user uuid);
-- SIP profiles (internal 5060, external 5080...)
create table v_sip_profiles (
	sip_profile_uuid uuid primary key, sip_profile_name text, sip_profile_hostname text, sip_profile_enabled text,
	sip_profile_description text, insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
create table v_sip_profile_settings (
	sip_profile_setting_uuid uuid primary key, sip_profile_uuid uuid, sip_profile_setting_name text,
	sip_profile_setting_value text, sip_profile_setting_enabled text, sip_profile_setting_description text,
	insert_date timestamptz, insert_user uuid, update_date timestamptz, update_user uuid);
