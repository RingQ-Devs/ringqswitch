# RingQswitch web interface: plain-English guide

The web interface lets you run the phone system from a browser: gateways
(SIP trunks), DIDs (your phone numbers), extensions, queues, IVR menus and
reports.

Version 0.10.1 is a documentation and branding release: every guide,
sample file, script and portal text names only RingQswitch; the API
reference lists every command, event and WebSocket message, and every port
RingQswitch listens on (docs/API.md, section 11). The speech recogniser is
installed as `rq-speech` and `rq-speech-server` (installs from earlier
versions keep working; run the install script again to rename them), the
AI service key can be set as `RQ_AI_API_KEY`, the interoperability tests are
in `test/interop/` (`make interop`), and the certificate helper is
`packaging/use-certificate.sh`.

Version 0.10.0 adds (laid out as in the RingQ portal):

- **Settings → Language & office hours** (section 14A): your company's
  language and time zone (with the time there now), **office hours** and
  **break hours** as day cards (click a day: open all day, these hours,
  closed) with what happens to calls outside them (hang up, busy, or any
  destination), **holidays** (one day or several, every year or once, all
  day or some hours, each with its greeting) with a default holiday sound
  (upload MP3 or WAV), and the IVRs, queues and agents that stay open on
  holidays. This applies to calls coming in on your DIDs.
- **Settings → Security** has tabs, as in RingQ (section 10):
  - **Allowed countries**: country cards by continent with flags and
    calling codes; calls out to an unticked country are refused (toll
    fraud protection).
  - **PIN-based dialing**: calls out (all, or numbers starting with 00)
    ask for a PIN; the PIN's account code is kept with the call.
  - **Webapp & mobile restriction**: Allow / Deny addresses for the web
    interface, and **auto logout** after a time without use.
  - **Anti-hacking**: **Enable global IP restriction** (only allowed
    addresses, your trunks and carriers may send SIP) and Allow / Deny
    address lists.
  - **SIP Guard**: protections that ban an address after repeated failures
    (wrong passwords, unknown users, calls to numbers that do not exist,
    call flooding, scanners on the web interface, web traffic floods,
    failed web sign-ins), each with its failures, window and ban time;
    live bans (ban by hand, unban) and history and stats.

Version 0.9.1 added:

- **BLF and Parked beside the dialer**, as in the RingQ apps: a small
  **<** handle on the dialer's left edge opens two tabs, **BLF** and
  **Parked** (with how many calls are parked). A tab slides its list out
  to the left of the dialer: your keys and the company's with their lamps,
  or the five parking slots with who is parked and for how long. Click
  the handle again to fold it away; the dialer remembers whether it was
  open. This replaces the dialer's Keys tab of 0.9.0 (section 16, "The
  dialer's BLF and Parked tabs").

Version 0.9.0 added (from the RingQ desk phone keys guide):

- **Busy lamp fields (BLF) for desk phones**: phones can now subscribe to
  an extension's state (the lamp shows idle, ringing with the caller, or on
  a call), to a parking slot (lit while a call is parked there), to their
  voicemail (message lamp) and to DND / forwarding (the phone's DND key
  stays in step with the apps). Pressing a ringing BLF key picks the call
  up.
- **Phone keys on the extension page** (Extensions → an extension → Phone
  keys): Extension (BLF), Speed Dial, Shared Parking (slots 1-5), Agent
  Status, Change Status and Blank, stored as RingQ keeps them, with a
  **Reboot phone** button. **Settings → Phone keys**: the company's keys,
  shown in every dialer.
- **The dialer's keys** (in 0.9.1 the BLF and Parked tabs beside it): your
  keys and the company's with their lamps, and the five parking slots (who is parked and for how long). A ringing
  extension key picks the call up; during a call an extension key transfers
  it and a parking slot parks it; a lit slot gives the parked call back;
  status keys set your agent status. The in-call **DTMF** pad also sends a
  whole sequence.
- **Dial codes** (section 16): *8 + extension pickup, *591-*595 parking
  slots, *26 + number agent status, *78 / *79 / *77 do not disturb, your
  own extension for voicemail (with a voice menu to listen, save and
  delete), *88 + team voicemail, *69 call return, *870 redial.

Version 0.8.0 added:

- **AI agents** (menu **AI agents**, see section 12E): a number answered
  by a voice AI. Callers hear your greeting and then simply talk: the
  agent answers at once, in a natural voice, from what you give it (a
  prompt, context, questions and answers, and your web pages, which it
  reads by itself), in the caller's language. When callers ask for a
  person (a *connect keyword* such as "agent" or "sales", or by pressing
  0) it says so and connects them. Each call is listed with what was said.
- **Send a DID to an AI agent in Call Flow**: drag the number onto the IVR
  card and choose the AI agent (IVR keys, office hours and queue timeouts
  can go to one too).
- **Call Flow's + opens the form in a window**: a new IVR menu, AI agent,
  queue, extension, ring group, conference room, office hours rule or fax
  extension is made without leaving Call Flow, and appears on its card at
  once.
- **Settings → AI agent service**: the voice AI service's API key (stored encrypted),
  and a connection test.

Version 0.7.4 added:

- **Queue calls given up while an agent's phone rang show as missed**
  too (0.7.3 still showed them as answered): a queue call that no agent
  answered, and that was not put through to anyone, is *missed*, however
  the caller left. Earlier calls are shown the same way.
- **Call records carry the queue's result**: when the caller hangs up
  while an agent's phone rings, the record now waits for the queue to
  cancel the ringing, so it says why the caller left
  (`cc_cancel_reason`, `cc_queue_canceled_epoch`) for the RingQ portal's
  reports too.
- The missed-call notification shows the number once when the caller has
  no name.

Version 0.7.3 added:

- **Queue calls nobody answered show as missed**: a queue picks up each
  caller to play its music, and Call history counted that as "answered",
  even when the caller hung up before an agent answered. Such calls are
  now *missed* (and counted as missed calls), like in the Queue calls
  report where they are *abandoned*; calls that end with a voicemail
  message are *voicemail*. Earlier calls are shown the same way (the
  records themselves are not changed).

Version 0.7.2 added:

- **Clean live text on noisy lines**: on a line with a steady background
  (a mobile caller in a noisy place), live transcription took the noise
  for one long sentence: the caller's words came 8 seconds late, mixed
  with invented words ("Je suis très très très..."). It now tells the
  background from speech, so each sentence is written as soon as the
  speaker pauses, and noise or a buzz is not sent to the recogniser.
- **Text the recogniser is unsure of is left out**: guesses over noise,
  words in the wrong language, repeated words ("très très très..." is cut
  short) and video-caption phrases ("Thanks for watching!").
- **One language per call**: with *Language spoken* set to *Detect
  automatically*, the call's language is fixed by its first clear sentence
  (before, each short phrase was guessed again, and noise was often
  "heard" as French). Choosing the language gives the best live text from
  the first word.
- **The live transcript panel no longer overlaps the dialer after it is
  closed and opened again**, and the Transcript tab shows on the dialer's
  edge without having to move the dialer.

Version 0.7.1 added:

- **Live transcription is much faster, and shows words while people
  speak**: each sentence is recognised about 7 times faster (the live
  recogniser now works on just the length of the sentence instead of a
  30-second window, without retries), the words so far appear in the
  speaker's bubble about every second, and the live recogniser is started
  and loaded as
  soon as live transcription is on, so the first words of a call do not
  wait for the model to load. Settings → Call transcription shows whether
  it is **Running**, and suggests Base when the live model is a larger,
  slower one (the install script now downloads Base for it too).
- **The live transcript panel no longer slides under the top bar**: it is
  exactly as tall as the dialer, beside it (on its right when the dialer
  is at the left edge), and takes the dialer's place when the dialer is
  closed.
- **Move the dialer**: drag it by its top bar anywhere in the page; it
  stays where you leave it (also after a reload). Double-click its top bar
  to put it back in the corner.
- **The dialer opens out of the round button** and shrinks back into it,
  and has a light border with a soft shadow all around.

Version 0.7.0 added:

- **Live transcription during calls**: while a call goes on, what both
  of you say is written down as you speak, in a **Live transcript**
  panel that slides out on the left of the dialer when the call is
  answered: the other party on the left, you on the right, three dots
  while someone is talking, then their words typed out. Closed, a
  **Transcript** tab beside the dialer (with the number of new lines)
  brings it back; **Copy** keeps the conversation (it is not saved).
  It works for desk phones and apps too, as long as the extension's web
  page is open. Turn it on in Settings → Call transcription → **Live
  transcription during calls**; run the install script once more to add
  the live recogniser, which keeps the speech model loaded so the text keeps
  up with the conversation. Section 12D, "Live transcription".

Version 0.6.3 added:

- **Voicemail transcription**: each message left in a voicemail box is
  written down on your server (Settings → Call transcription →
  **Transcribe voicemail messages**, on by default once transcription is
  set up; it works even with call transcription off). The text shows on
  the **Voicemail** page and in the dialer's **Voicemail** tab; a click
  opens the message with its recording (a click on a line plays from
  there), its sentiment, Copy, Download and **Transcribe again**. The text
  is also saved where the RingQ database keeps it
  (`v_voicemail_messages.message_transcription`), so the RingQ portal can
  show it. Older messages: **Transcribe** in their row.
- **Voicemail buttons like the reports**: play (stop while playing),
  download and delete are icons; dates are compact; 50 messages per
  page.

Version 0.6.2 added:

- **Queues send callers on when nobody can answer**: an agent counts as
  there only when Available with a phone online (a desk phone, an app or
  the browser dialer). Agents on a break (also "Max no answer"), logged
  out or with no phone online do not; agents busy on a call do (callers
  wait for them). When nobody is there for the queue's "Leave when no
  agent is available for" time (90 s by default), callers hear "Sorry, no
  agent is available to take your call at the moment" and go to the
  queue's **Then go to** (an extension's voicemail, for example). Before,
  a break counted as "there" and callers waited until they hung up.
  Section 12.2, "When does a queue call go to voicemail?".
- **Agents with no phone online are not rung**, so they no longer collect
  missed calls and drop into "Max no answer" for nothing. The wallboard
  shows them as **Phone offline**, and **Logged In** counts agents logged
  in *with a phone online*.
- **Voicemail greets callers**: a box without a recorded greeting says
  "The person you are calling is not available. Please leave a message
  after the tone." (before: only the beep). Upload a prompt named
  `voicemail-greeting`, `queue-no-agent` or `queue-timeout` in Sounds &
  music to use your own recording.
- **Notifications close by themselves** after a few seconds (missed
  calls after 10 s); only a ringing call and a meeting guest waiting stay
  until answered.
- **Names**: the top bar, the user menu and the dialer show the
  extension's first and last name ("Thilak Manager", avatar **TM**) as
  soon as it is saved, without signing in again; the wallboard shows
  people's names. The avatar in the user menu is centred again.

Version 0.6.1 added:

- **Transcripts keep the speakers in order**: each speaker's turn is
  recognised on its own, so what was said stays where it was said (in
  0.6.0 the recogniser's timing could put an answer before its question).
  Steady hums and buzzes (a phone vibrating next to a microphone) are no
  longer taken for speech. Transcribe an earlier call again from its
  transcript window (**Transcribe again**). Section 12D.
- **Choose the speech model** (Settings → Call transcription → Speech
  model): every speech model, from Tiny to Large v3, with its size
  and speed; the administrator downloads one with a click (progress
  shown) and deletes it again. The page's counts update live.
- **Call history**: click a call to see its path (where it came from and
  through which trunk, the IVR menus and keys pressed, the queue or ring
  group, who rang, who answered, who hung up); play and download are
  icons; the row says who answered and who hung up; narrower Queue and
  date columns, so the table fits without scrolling sideways.
- **All reports**: 50 rows per page with page numbers, and **Period →
  All** (the default) for the whole history. Export CSV still exports
  every row of the period.

Version 0.6.0 added:

- **Call transcription with sentiment** (Settings → **Call
  transcription**): after each call, RingQswitch writes down who said what
  (**Speaker A**, the caller; **Speaker B**, who answered) and how they
  felt: each line, each speaker and the whole call are marked Positive,
  Neutral or Negative, and on calls through a trunk the outside party is
  tagged **Customer** and the other **Agent**. **Call history** and the
  **Queue calls** report get a **Transcript** column (the icon opens the
  conversation, with the recording: a click on a line plays from there)
  and a **Sentiment** column. Everything runs on your server (the speech
  recogniser, installed once with `packaging/install-transcription.sh`); no call audio
  leaves it. Section 12D.
- **Esc** closes any window of the web interface.

Version 0.5.14 added:

- **eFax for an extension**: drop a DID on **Call Flow → Others** and
  choose an extension (marked **eFax**): the number answers as a fax
  machine for that extension and the faxes arrive in the eFax **Inbox**.
  The DID shows in the Others card as "→ eFax · 1000 Ext 1000". Dropped
  on **Extension**, the same extension gets the number's calls instead.
  (In 0.5.11-0.5.13 an extension chosen under Others became a call route
  and moved to the Extension card; assign such a number again.)
- **The browser dialer uses the microphone only during calls**: the
  browser's red recording dot shows while you dial, talk or hold, and
  while the dialer's Settings shows your voice level, not all the time.
  The microphone permission is still asked once, when the dialer starts.
- The menu's collapse button shows whole (the top bar covered half of it)
  and turns blue when the mouse is over it.
- Dashboard: waves leave the phone while no call is in progress, the
  Active Calls dot breathes while calls are live, and the cards rise in
  when the page opens (no motion with the system's "reduce motion").

Version 0.5.13 added:

- **T.38 fax**: when the carrier switches a fax call to T.38 (its fax
  gateway re-INVITEs, typically 2-3 seconds into the call when it hears
  the fax tones), RingQswitch accepts and the fax goes on over T.38
  (UDPTL, on the call's media port). Before, RingQswitch refused, and some
  carriers then stopped the call's audio until they hung up 32 seconds
  later ("The call dropped prematurely"). `fax_enable_t38=false` on a call
  keeps the fax as audio. Section 12C.
- **SIP answers reach carriers that send from one port and listen on
  another** (RFC 3261 18.2.2): a reply goes to the port in the request's
  Via header, unless the sender asked for `rport` or is behind NAT. Before,
  such a carrier never received the answer to its re-INVITE.
- **RingQswitch names**: calls show as `ringq/gateway/<trunk>/<number>`,
  `ringq/internal/<user@domain>` and `ringq/external/<uri>` in the log,
  `show channels` and events. Generated outbound routes and eFax dial
  `ringq/gateway/...`. Existing dialplans that use the older spellings
  (`sofia/gateway/...`, `sofia/internal/...`) still work. In `rq_cli`,
  `sip status` shows the trunks (`sofia status` still works, for the
  portal).

Version 0.5.12 added:

- **Fax troubleshooting**: every fax call writes what it hears to the log
  every 5 seconds (level, and which fax tones: CED, V.21...), each fax
  message sent and received, and a summary at the end. When a fax fails,
  the call's audio is kept, and the **Status log** shows two download
  buttons (ear: what the far end sent; speaker: what RingQswitch sent).
  A trunk asking to switch to T.38 is logged too. Section 12C.

Version 0.5.11 added:

- **Fax DIDs answer as a fax machine themselves**: a DID assigned to a fax
  extension gets the dialplan RingQ's fax DIDs use
  (`fax_direction=in`, `fax_file`, `answer`, `stop_record_session`, `rxfax`,
  `hangup`). Changing the fax extension rewrites its DIDs. Section 12C.
- **Call Flow → Others** also offers every extension when you drop a DID.

Version 0.5.10 fixed:

- **Fax extensions on RingQ databases**: "the database has no v_fax table"
  when saving a fax extension. RingQswitch now creates the `v_fax` table by
  itself when it starts, like its other tables.

Version 0.5.9 adds:

- **eFax** (menu **eFax**, owners and managers), like RingQ's: send a PDF,
  TIFF or image as a fax from one of your DID numbers (it goes out on that
  number's trunk), and receive faxes on a **fax extension**: assign a DID
  to it in **Call Flow → Others → Fax**, and what arrives is a PDF in the
  **Inbox**. Tabs Overview, Send eFax, Inbox, Status log and Settings
  (station id and page header). Section 12C.
- **Fax is built into RingQswitch**: the `rxfax` and `txfax` applications
  (the audio way, "T.38 off", through any G.711 trunk) with the variables
  and events RingQ's dialplans and fax workers expect, so they work
  unchanged. Sending a PDF needs the server's PDF converter
  (`sudo apt install ghostscript`).
- Phones: the top bar fits the screen without scrolling sideways.

Version 0.5.8 added:

- **Single sign-on**: sign in with **Microsoft**, **Google** or
  **LinkedIn** accounts, set up on **Settings → Integrations** like
  RingQ's (provider cards, setup steps and the Redirect URI to copy). The
  account's email picks the extension (its new **Sign-in email** field, its
  RingQ user's email or its voicemail email). Web passwords can be turned
  off per domain. Section 12B.
- **Country on DIDs**: the DID window (Call Flow and the DID page) has a
  **Country**; calls match the number with or without its country code.
- **Assign** instead of Link when giving a DID to an IVR, queue, extension
  or other.

Version 0.5.7 added:

- **Signing in to the portal logs the agent in.** When an extension that
  is a queue agent signs in, its agent becomes **Available** (or the
  status the portal's sign-out took from it), so the wallboard's
  **Logged In** counts everyone who is signed in. Before, an agent that
  was already Logged out (after a restart, or logged out from the dialer
  or the wallboard) stayed Logged out after signing in.
- **Signing out of one browser** no longer logs the agent out while the
  same extension is still signed in in another browser (or signs in
  again within a few seconds). Signing out everywhere still logs it out,
  unless a desk phone is registered as the extension.

Version 0.5.6 added:

- **Call Flow drag and drop works both ways**: drag a DID from Available
  DID Numbers onto IVR, Queue, Extension or Others, and drag a linked DID
  back onto **Available DID Numbers** to unlink it (the card lights up
  "Drop here to make the number available again"; a window asks you to
  confirm). A linked DID can also be dragged from one card to another.

Version 0.5.5 added:

- **Call Flow** (menu **Call Flow**, like RingQ's): SIP trunks, IP gateways
  and DID numbers are on one page, and every change happens in a window.
  A new DID waits under **Available DID Numbers**; **drag it onto
  Interactive Voice Response, Queue, Extension or Others** (ring group,
  conference, office hours, voicemail, hang up) and the window that opens
  lists the IVRs / queues / extensions to choose from. The trash on a
  number unlinks it (it goes back to Available). Section 12.
- **Hotel front desk** (menu **Hotel front desk**): every extension is a
  room. **Check in** a guest (first and last name), **edit** the guest,
  **check out**, and **schedule a wake-up call**: at that time the room's
  phone rings and plays the wake-up sound; not answered, it rings again 5
  minutes later (3 tries). Section 12A.
- **The dialer fits without scrolling**: on a short window the keypad and
  the call screen get smaller (smaller keys and buttons, then no picture,
  then no letters) instead of scrolling.

Version 0.5.4 added:

- **rq_cli console**: `sudo rq_cli` opens the console with a banner and
  the **live log in colour**, without typing a password (it is read from
  `/etc/ringqswitch/ringqswitch.xml`). It shows `New Channel`, `receiving invite from`, `SIP auth challenge`, `Processing ...
  in context ...`, the **dialplan trace** (`Dialplan: ... Regex (PASS) ...`,
  `Action ...`), `EXECUTE ...`, `Callstate Change`, `Hangup ...
  [NORMAL_CLEARING]`, `Close Channel`, registrations and queue events.
  `/log info` (less), `/log debug` (everything), `/nolog`, `/events`,
  `/help`, `/exit`; `rq_cli -x "show channels"` runs one command.
- **Web password visible**: on the extension page the web password shows
  as dots with an eye to see it (owners). A password set before 0.5.4
  appears after that user signs in once more (until then the field says
  so); changing it works as before.
- **The extension registers by itself**: after signing in, the browser
  dialer starts on its own (no "Start the dialer" click). Turning the
  dialer off with its switch keeps it off.
- **Wallboard "Logged In"** follows the portal: signing out logs the agent
  out of its queues when no other phone is signed in as its extension (a
  desk phone keeps it in), and signing in again puts it back.
- **Dialer without a scroll bar** during calls; the top bar's menus open
  over the dialer.
- **Reports: real wait times**. A queue call's wait is its time in the
  queue (it was 0:00 because the queue answers at once) and its talk time
  starts when the agent answers; other calls wait until they are answered
  by a person. Older call records of queue calls show their real wait too.

Version 0.5.3 added:

- **Top bar like RingQ**: calendar (Meetings), Meet, Chat, **reload
  (clears the RingQswitch cache)**, **tower (Call status)**, Wallboard,
  **globe (language)**, light/dark, and your name.
- **User menu** (click your name): your name and domain, your extension
  with its status (Online, On break, Do not disturb), **Connected
  devices** (the phones and apps signed in as your extension, with a
  refresh button), View profile, Logout, the version and **Previous
  builds**.
- **Reload = clear the cache** (Owners): RingQswitch forgets what it keeps
  in memory (carriers' addresses, number patterns) and reads everything
  again from the database at once (`xml_flush_cache` + `reloadxml`). Calls
  in progress continue. Use it after changing something
  in the RingQ portal.
- **Call status** (tower; Owners and Managers): **Active calls** and
  **Online extensions** tabs, a search box, the legend (Queue, Inbound,
  Outbound, Fax, Internal), each call's direction, from → to, queue,
  answered by, duration, status and route, **Listen / Whisper / Barge in /
  Hang up**, and today's **Previous calls** with wait / duration and the
  queue result (e.g. "9007 - Queue Answered", "Queue Caller Abandoned").
- **Languages**: English, Arabic (right to left), French, Spanish,
  Portuguese (Brazil), German, Chinese (Simplified), Japanese, Korean,
  Vietnamese, Malay, Tamil and Hindi. The menu, top bar, user menu, page
  titles, table headings, common buttons, Call status, notifications and
  the sign-in page change language; longer help texts stay in English.
- **Notifications at the top right** for chat messages (Reply), incoming
  calls (Answer / Decline), missed calls (Call back), new voicemail and
  people asking to join your meeting (Admit / Decline).
- **Meetings: the organizer lets people in.** The waiting room is on by
  default and applies to everybody, colleagues too. Only the person who
  made the meeting is its host; attendees wait until the organizer is
  there and admits them. The organizer can turn the waiting room off in
  the meeting's host controls.
- **SIP Profile**: when your database has no SIP profile yet (the tables
  but no rows), a button adds RingQ's **standard profiles**:
  `internal` on the main port and `external` on 5080. Every profile can be
  edited.
- **Passwords hidden**: every password field shows dots, with an eye to
  show it (SIP password, web password, voicemail PIN, gateway password,
  Phone settings).
- The side menu keeps **one group open at a time**, and all scroll bars are
  thin.

Version 0.5.2 added:

- **Global Settings** (Settings → Global), like RingQ: tiles for Outbound
  Rules, Office Hours, Music On Hold, Security, Service Restart, Domains,
  Aux Codes, Queues, Contacts, **Network**, **SIP Profile**, Dialplans,
  Gateways, User Roles, Audit Logs, Call History, Call Landing (DIDs) and
  Reports.
- **Outbound rules** with RingQ's form: rule name; calls **from all or
  selected extensions** and **extension ranges** (e.g. 100-199); calls
  **to numbers starting with a prefix** and **of a length**; **Route** (a
  gateway, or **BLOCK**) with a backup route; **strip digits**,
  **prepend**, **outbound caller ID** (one of your numbers); and the **SIP
  from user, display name and host** and **CID type** (PAI, RPID, none).
- **SIP Profile**: like RingQ's internal (5060) and external (5080)
  profiles. Every enabled profile now **really opens its port**, answers
  from it, and gateways set to that profile send from it (carriers that
  send calls to 5080 work). Add, edit and delete profiles.
- **Network** page: this server's SIP and audio addresses and ports, and
  the exact firewall commands for them.
- **Wallboard: Queue Details** under Agent Details: per queue the calls
  answered, abandoned and missed today, % answered, callers waiting,
  average waiting and talking time, agents and busy agents. Every wallboard
  section has −, + and full-screen buttons.
- **Database tools' "permission denied"** fixed: RingQswitch now lets the database
  owner and the owner of the RingQ tables read and change its own tables
  (and any user listed in `grant-to`).
- Menus at 13 px, and a more compact **Team directory**.

Version 0.5.1 added:

- **Meet window like RingQ**: the camera button at the top opens **Meet**
  inside the portal (minimise, maximise, close). **Start** a meeting (title,
  waiting room, guests, passcode) or **Join** one with a link or code. You
  see yourself first, with **Mic**, **Camera** and **Effects**, and
  **Devices and audio** to pick the microphone, camera and speaker.
- **In the meeting**: Copy link, Record, **Transcribe** (live captions and a
  transcript), **Present file** (a PDF or picture everyone sees), a
  **whiteboard** (the pen), reactions, raise hand, chat, people, settings
  and host controls. Someone in the waiting room shows as "… wants to join"
  with **Admit** and **Decline**.
- **Meeting links** look like `https://wasip.ringq.io:8443/meet/m/46ak-c6kg-hvcz`.
  Guests see **Ready to join?**: name, email (optional), the code, and
  **Ask to join** (or **Join now** when there is no waiting room).
- **Recent meeting content**: after a meeting, who came, the chat and the
  transcript are kept (Meet window → Start → Load recent meeting content).
- **Dialer: Audio check**. During a call, **Audio check** shows the four
  directions of the sound (your mic → server → other side, and back) and
  says where it stops. Every call also leaves a "dialer: call audio" line
  in the log, and the switch warns when one side of any call sends no audio
  ("one-way audio").

Version 0.5.0 added:

- **User roles page** (Accounts → User roles), like RingQ: four groups
  (User, Manager, Owner and No portal access). **Drag a person** to another
  group to change what they can do. A new **Manager** role is for
  supervisors: they can listen, whisper, barge in, divert and hang up calls,
  set any agent's status and see all reports, but they cannot change
  settings.
- **Contacts** (Contact center → Contacts): the company's shared address
  book. Add, edit, search, call, and **Import / Export** CSV files. The
  dialer's Contacts tab shows them too, and calls from a saved number show
  the contact's name.
- **Team directory** like RingQ: a coloured card per person (green
  available, amber busy or on a break, red unregistered), a "YOU" badge,
  Call / Chat / Favourite buttons, a filter (All, Available, On a call,
  Unregistered, Favourites), search and a list view.
- **RingQ look**: the Inter font (built in, no internet needed), RingQ's
  text and menu colours, light-blue table headers, blue highlighted
  sub-menu items, and search, pages and "rows per page" on every list.
- **Dialer audio settings** (dialer → Settings): choose the microphone and
  speaker (e.g. your headset), a **Microphone boost** for a quiet
  microphone, **Speaker volume**, a live **voice level** meter, and the
  **Audio path** (UDP is best; TCP means UDP 8443 is blocked somewhere and
  adds delay). The server also queues much less audio on TCP, so a slow link
  drops old audio instead of building up delay.

Version 0.4.2 fixed the dialer and meeting audio:

- **No audio in either direction** ("Dialer audio could not connect"). This
  happened on servers where `ext-rtp-ip` or `public-ip` is set, which is
  usual on cloud servers: the audio session was tied to the server's
  loopback socket instead of the public one. Fixed.
- **Audio also works where UDP is blocked.** If UDP port 8443 is closed (by
  ufw, the cloud firewall or an office network), the browser now sends the
  audio over **TCP on the web port** (the same 8443 the page uses), so it
  still works. UDP is still used first when it is open, because it gives
  the best sound.
- When the audio cannot connect at all, the dialer shows a **red box** with
  the exact address it tried and what to open, and a **Try again** button.
- The **call timer** in the dialer now uses the server's clock, like the
  dashboard. It was wrong when the PC's clock was off.
- On the dashboard, **To** no longer repeats the caller's number, and the
  Listen / Whisper / Barge in / Divert / Hang up buttons wrap instead of
  needing a scroll bar.

Version 0.4.1 gave the portal a clean new look, like the RingQ portal:

- A white **menu on the left** with icons and groups (Accounts, Contact
  center, Communicate, Call Flow, Reports, Settings). It has a **Search**
  box (**Ctrl K**) and a button to fold the menu down to icons only.
- **The dialer is a round phone button at the bottom right.** Click it and
  the dialer opens above it, with its own tabs: Recents, Voicemail, Keypad,
  Contacts and Settings.
- **Dashboard** like RingQ: Active Calls with Inbound and Outbound counts,
  plus System Health (CPU, memory, disk), Extensions (registered and
  unregistered), Trunks, System Information and Recent calls.
- **Wallboard** like RingQ: big tiles (Call Waiting, Active, Total Calls,
  Abandoned, Agent Busy and more), a **Queue Calls** table and an
  **Agent Details** table with a log-in switch per agent.
- **Team directory**: everyone in your company, with their status and Call
  and Chat buttons.
- **Dark mode** (moon button at the top), remembered per browser.
- Fixed: the red error "Failed to execute 'setRemoteDescription'… wrong
  state: stable". It appeared when the dialer started again after a page
  reload.

Version 0.4.0 added:

- **Sign in with your extension number** and a separate **web password**.
  The SIP password stays for your phones only.
- **Roles**: **Owner** sees and changes everything; **User** gets the dialer,
  live monitoring, their own reports, chat and meetings.
- **A dialer inside the portal.** Your browser becomes a phone for your
  extension: calls, hold, transfer, keypad, contacts, voicemail and your
  call-centre status (Available, or a break with an AUX code).
- **Live dashboard** with **Listen, Whisper, Barge in, Divert and Hang up** on
  every answered call.
- **Wallboard (TV)**: a live call-centre screen for a big TV.
- **Chat**: one-to-one and group chat between extensions, with files.
- **Meetings**: video meetings in the browser, with screen sharing, a
  waiting room, guests from outside, chat, hand-raising and recording.
- **Security page**: blocked addresses, IP rules (deny, never-block,
  web-interface allow-list), SIP scanner blocking and the carrier ACL.
- **Your own SSL certificate** (a free certificate for e.g.
  wasip.ringq.io) with automatic renewal.

Copy and paste the commands exactly. Lines starting with `#` are
explanations; you do not type them.

---

## 0. Already on 0.4.x or 0.5.x? Upgrade to 0.5.12 (2 minutes)

Nothing changes in your settings. RingQswitch adds its new tables by itself
when it starts. On the server, logged in as root, use **one** of the two
packages:

**A. From the source package** (what you use now), after uploading
`ringqswitch-0.5.12-src.tar.gz` to `/root`:

```bash
cd /opt
tar xzf /root/ringqswitch-0.5.12-src.tar.gz
cd /opt/ringqswitch-0.5.12
make build
sudo make install
sudo systemctl restart ringqswitch
ringqswitch -version
# should print: RingQswitch 0.5.12
```

**B. From the ready-made package**, after uploading
`ringqswitch-0.5.12-linux-bin.tar.gz` to `/root`:

```bash
cd /opt
tar xzf /root/ringqswitch-0.5.12-linux-bin.tar.gz
cd /opt/ringqswitch-0.5.12-linux
sudo bash packaging/install.sh
sudo systemctl restart ringqswitch
ringqswitch -version
# should print: RingQswitch 0.5.12
```

**New in 0.5.9: eFax.** To fax PDFs, install the PDF converter once (TIFF
and images work without it):

```bash
sudo apt install -y ghostscript
```

If the RingQ fax engine (`ringq-fax-worker`) runs on this server, stop it
so that only RingQswitch sends the queued faxes (both use the same
`fax_jobs` table, so the history stays):

```bash
sudo systemctl disable --now ringq-fax-worker ringq-fax-api 2>/dev/null
```

Then: **eFax → Settings → Manage fax extensions → New fax extension**, and
in **Call Flow** drag your fax DID onto **Others → Fax** (section 12C).

**New in 0.5.3: no SIP profile in your database?** Open **Settings → Global
→ SIP Profile** and click **Add the standard profiles** (internal on the
main port, external on 5080). Then open 5080 in the firewall as shown
below.

**New in 0.5.2: SIP profiles open their ports.** If your RingQ database
has an enabled `external` profile (port 5080), RingQswitch now listens on
5080 too. See which ports are open under **Settings →
Global → SIP Profile**, and open them in the firewall:

```bash
sudo journalctl -u ringqswitch -n 50 --no-pager | grep "SIP profile"
# SIP profile listening profile=external port=5080
sudo ufw allow 5080/udp && sudo ufw allow 5080/tcp
```

Then check that the browser audio port is ready:

```bash
sudo journalctl -u ringqswitch -n 30 --no-pager | grep "dialer and meetings audio"
# shows: udp=8443 tcp=8443 public_ip=<your public IP or empty>
```

If `ringqswitch -version` still prints an older version, the old program is
still running: `sudo systemctl restart ringqswitch` and check again.

3. Open UDP 8443 if you have not yet. Audio then uses UDP, which sounds
   best. Without it, audio still works over TCP 8443.

```bash
sudo ufw allow 8443/udp
```

   Using your cloud provider's firewall too? Add an inbound rule for UDP
   8443 there as well.

4. Open the phone audio ports too (calls to and from phones and your
   carrier):

```bash
sudo ufw allow 16384:32768/udp
```

   Again, add the same inbound UDP range in your cloud provider's firewall
   if you use one. When these are closed, you hear nothing from the other
   side of outside calls (one-way audio).

5. In the browser, reload the portal (Ctrl+F5) and make a test call from
   the dialer. During the call, press **Audio check** under the call
   buttons (section 5). After hanging up, the server log says how the
   audio went:

```bash
sudo journalctl -u ringqswitch -n 200 --no-pager | grep -E "dialer: (audio|call audio|one-way)|no audio received"
# dialer: audio connected user=1000@... via=udp            (browser <-> server, best)
# dialer: call audio ok ... from_browser=... from_call=...  (sound went both ways)
# dialer: one-way or no audio ... problem="No sound reaches the server from the other side ..."
```

Your certificate setup stays as it is. Do **not** run
`use-certificate.sh` or `setup-test-db.sh` again.

---

## 1. Upgrade your server from 0.3.0 to 0.5.12 (about 10 minutes)

Your 0.3.0 server keeps its database, settings and phones. Nothing is
deleted. **Do not run `setup-test-db.sh` again**: it is only for a new
server, and with `--reset` it deletes the test database.

### Step 1: Put version 0.5.12 on the server

**Option A (easiest, no Go needed): the prebuilt package**

1. Download `ringqswitch-0.5.12-linux-bin.tar.gz` from this chat.
2. Upload it to the server's `/root` folder with your file transfer
   program (SFTP), as you did before.
3. On the server, logged in as root:

```bash
cd /opt
tar xzf /root/ringqswitch-0.5.12-linux-bin.tar.gz
cd /opt/ringqswitch-0.5.12-linux
sudo bash packaging/install.sh
# ends with: ==> keeping existing configuration in /etc/ringqswitch ... configuration OK
ringqswitch -version
# should print: RingQswitch 0.5.12
sudo systemctl restart ringqswitch
sudo systemctl status ringqswitch --no-pager
# should say: active (running)
```

**Option B: build from source** (needs **Go 1.24 or newer**)

1. Download `ringqswitch-0.5.12-src.tar.gz` and upload it to `/root`.
2. Check Go:

```bash
go version
# go1.24 or newer: fine. Older (e.g. go1.22) or "command not found": run these 5 lines
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -LO https://go.dev/dl/go1.24.7.linux-$ARCH.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.24.7.linux-$ARCH.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
go version
```

3. Build, install and restart:

```bash
cd /opt
tar xzf /root/ringqswitch-0.5.12-src.tar.gz
cd /opt/ringqswitch-0.5.12
make build
sudo make install
ringqswitch -version
# should print: RingQswitch 0.5.12
sudo systemctl restart ringqswitch
```

The new tables (web logins, chat, meetings, security rules) are created by
themselves when RingQswitch starts.

### Step 2: Open the port for browser audio and video

The dialer and meetings send audio and video over **UDP port 8443**. The web
page itself keeps using **TCP port 8443**.

```bash
sudo ufw allow 8443/udp
sudo ufw status
# 8443/tcp and 8443/udp must both be listed
```

> Using your cloud provider's firewall too? Add an inbound rule for
> UDP 8443 there as well.

### Step 3: Give extension 1000 a web login (Owner)

Pick a web password of at least 8 characters, different from the SIP
password. Replace `MyWebPass2024` with yours:

```bash
sudo RQ_WEB_PASSWORD='MyWebPass2024' ringqswitch -conf /etc/ringqswitch -web-access 1000@test.ringq.local -role owner
# prints: 1000@test.ringq.local can sign in to the web interface (owner)
```

For every other extension, do it from the web: **Accounts → User roles**,
drag the person into **User**, **Manager** or **Owner** and type a web
password. (Or open the extension under **Accounts → Extensions** and set
**Web portal access** there. On the command line, `-role` takes `owner`,
`manager` or `user`.)

### Step 4: Use your SSL certificate (wasip.ringq.io)

First check that the name points to this server. On the server:

```bash
getent hosts wasip.ringq.io
# must show 174.138.24.53
```

If it shows another address, change the DNS **A record** of `wasip.ringq.io`
to `174.138.24.53` with your DNS provider first. It can take a few minutes
to update.

Then run one command (the certificate files you mentioned,
`fullchain.pem` and `privkey.pem` in
`/etc/letsencrypt/live/wasip.ringq.io/`, must be on this server):

```bash
cd /opt/ringqswitch-0.5.12-linux      # or /opt/ringqswitch-0.5.12 if you used Option B
sudo bash packaging/use-certificate.sh wasip.ringq.io
```

It copies the certificate where RingQswitch can read it and switches the web
interface to it. It also installs a renewal hook, so renewed certificates
are picked up within a minute, with no restart. The end of
the output shows the address:

```
Done. Open: https://wasip.ringq.io:8443/
```

> **Want https://wasip.ringq.io/ without `:8443`?** If nothing else (such as
> another web server) uses port 443 on this server, run
> `sudo bash packaging/use-certificate.sh wasip.ringq.io --port 443` and
> allow it in the firewall: `sudo ufw allow 443/tcp`. If another web server
> already uses port 443, see section 11.3 to put RingQswitch behind it.

### Step 5: Sign in

1. Open **https://wasip.ringq.io:8443/** (or https://174.138.24.53:8443/ if
   you skipped Step 4; then accept the certificate warning once).
2. User: **1000**. Password: the web password from Step 3.
3. You can still sign in as **admin** with the password in
   `/etc/ringqswitch/ringqswitch.xml` (`admin-password`). The admin account
   is for the whole server; it has no extension, so it has no dialer, chat or
   meetings.

> Several domains with the same extension number? Sign in as
> `1000@test.ringq.local`.

### New server instead of an upgrade

On a fresh server, follow STEP-BY-STEP.md. `sudo bash
packaging/setup-test-db.sh` now also gives extension 1000 Owner web access
(with the same password as admin) and opens UDP 8443.

---

## 2. Security: do this today

**a) PostgreSQL must not be open to the internet.** If you opened port 5432
for a database tool earlier, close it and use the SSH tunnel instead
(STEP-BY-STEP.md, Step 8):

```bash
sudo ufw delete allow 5432/tcp
sudo ufw delete allow 5432/udp
sudo ufw delete allow 5432
sudo sed -i '/^host *all *all *0\.0\.0\.0\/0/d' /etc/postgresql/*/main/pg_hba.conf
sudo sed -i "s/^listen_addresses *=.*/listen_addresses = 'localhost'/" /etc/postgresql/*/main/postgresql.conf
sudo systemctl restart postgresql
sudo ufw status
# 5432 must not be listed any more
```

**b) SIP scanners.** SIP Guard bans addresses that fail to sign in too
often, call numbers that do not exist, or flood the server, and blocks
well-known attack tools (friendly-scanner, sipvicious and others) on their
first packet. To block an address for good, use **Settings → Security →
Anti-hacking** (section 10): Action **Deny**, the address (e.g.
`54.38.94.109`), **Add**.

**c) Allow only your office to open the web interface** (recommended).
In **Settings → Security → Webapp & mobile restriction → IP**, add your
office's public IP (find it at whatismyip.com) with Action **Allow**.
RingQswitch refuses a rule that would lock out the address you are using.

**d) Stop toll fraud.** In **Settings → Security → Allowed countries**,
untick the countries nobody in your company calls (the satellite and
premium networks under **Other** first).

---

## 3. Signing in and roles

| Who | Signs in with | Can do |
|---|---|---|
| **admin** | `admin` + `admin-password` from ringqswitch.xml | Everything, in every domain. No dialer, chat or meetings (it is not an extension). |
| **Owner** extension | extension number + web password | Everything in their domain: all settings, all calls, all reports. Also Security, System, Domains and the Event log, unless the server administrator turned that off (see below). Dialer, chat and meetings. |
| **Manager** extension | extension number + web password | A supervisor: everything a User has, plus **Listen / Whisper / Barge in / Divert / Hang up** on every call, every agent's status (log in, breaks), **all** reports and the Devices online list. No settings: cannot change extensions, queues, gateways, roles or security. |
| **User** extension | extension number + web password | Monitoring (**Dashboard**, **Call centre (live)**, **Wallboard**, **Team directory**), **Reports** (only their own calls and agent records), the **Dialer**, Chat, Meetings, their own voicemail and **My account**. No settings, and no Listen, Barge, Divert or Hang up on other people's calls. |

- The **web password** is separate from the SIP password. Changing one never
  changes the other.
- Everyone changes their own web password under **Settings → My account**.
- An Owner sets or resets anyone's web password on the extension form.
- Choosing **No web access** on the extension form removes the login.
- **Accounts → User roles** (Owners) shows everybody in four groups: **User**,
  **Manager**, **Owner** and **No portal access**. To change someone's role,
  **drag their card** to another group. (Or click the card, or press Enter on
  it, and choose the group.)
  - Moving someone out of **No portal access** asks for a web password (one
    is filled in; write it down and give it to them).
  - Moving someone to **No portal access** asks to confirm; their phones keep
    working.
  - Your own card has a lock: another Owner must change your role, so you
    cannot lock yourself out.
  - A person whose role changes is signed out of the portal, and signs in
    again with the new role.
- After 10 wrong passwords within 10 minutes, that address must wait 10
  minutes before trying again.
- An Owner should not manage the whole server? Add
  `<param name="owner-server-access" value="false"/>` to the `<web>` section
  of `/etc/ringqswitch/ringqswitch.xml` and restart. Owners then keep their
  domain but lose Security, System, Domains and the Event log; only admin has
  them.

---

## 4. The screen

- **Left menu** (what you see depends on your role):

  | Menu | Pages |
  |---|---|
  | **Dashboard** | Live overview (section 6) |
  | **Team directory** | Everyone in your company and their status, with Call, Chat and Favourite buttons (section 4.1) |
  | **Accounts** | Extensions, User roles (section 3), Ring groups, IVR menus, Office hours, Conference rooms |
  | **Contact center** | Contacts (section 4.2), Call centre (live), Queues, AUX / break codes |
  | **Wallboard** | The call-centre screen for a TV (section 7) |
  | **Communicate** | Chat, Meetings, Voicemail |
  | **Call Flow** | SIP trunks, gateways and DID numbers, and where each number goes (section 12) |
  | **Hotel front desk** | Rooms, check-in / check-out and wake-up calls (section 12A) |
  | **Reports** | Call history, Agent status times, Agent status log, Queue report, Agent performance, Queue calls |
  | **Settings** (bottom) | Global, Outbound rules, Integrations (single sign-on), Sounds & music, Security, Domains, Dialplans, System, Event log, My account |

  - Click a group to open or close it.
  - **Search** (or **Ctrl K**): type part of a page name, e.g. "queue", then
    press Enter to open the first match.
  - The small **‹** button on the menu's edge folds the menu down to icons
    only; **›** opens it again.
  - At the bottom: your name, role and extension, and **Sign out**.
- **Top bar** (right side):
  - the **Domain** selector (admin only);
  - **Meetings**, **Chat** (with a red number for unread messages) and
    **Wallboard**;
  - the **moon / sun** button for dark or light mode;
  - your initials and name. The dot is green when your dialer is
    registered, amber on a break, red with do-not-disturb or a dialer
    problem. Click it for **My account** and **Sign out**.
- **The round phone button at the bottom right** opens the dialer
  (section 5). Its dot shows the same colours.
- On a phone or small screen, the menu opens with the **☰** button.
- Every list (Extensions, Queues, Gateways…) has a **search box**, and pages
  with **Previous / Next** and **Rows per page** once it is longer than 10.
- Changes apply **immediately**. There is no "apply" button and no restart.

### 4.1 Team directory

One card per extension, coloured by status:

| Colour | Status |
|---|---|
| Green | **Available**: a phone or the browser dialer is registered |
| Amber | **On a call**, **on a break** (with the AUX reason) or **Do not disturb** |
| Red | **Unregistered**: no phone or dialer is signed in |

Your own card says **YOU**. The others have **Call** (rings them from your
dialer), **Chat** and **Favourite** (a star; favourites come first, and the
filter can show only them). Use the **filter** (All, Available, On a call,
On break / do not disturb, Unregistered, Favourites), the **search** box,
and the button on the right to switch between cards and a list.

### 4.2 Contacts

**Contact center → Contacts** is the company's shared address book: name,
company, phone, mobile, email and notes.

- **New contact** adds one. Everyone in the company can add contacts.
- The phone icon calls the number from your dialer; a second icon calls the
  mobile.
- The pencil edits and the bin deletes. Owners, Managers and the person who
  added a contact can do this.
- **Search** finds names, companies, emails and numbers (type part of a
  number, e.g. 61234).
- **Import** (Owners and Managers): a CSV file from Excel, Outlook or Google
  Contacts. The first row names the columns: `name` (or `first name` and
  `last name`), `company`, `phone`, `mobile`, `email`, `notes`. Rows without
  a name or number, and numbers already in the address book, are skipped.
- **Export** (Owners and Managers) downloads all contacts as a CSV file.
- The dialer's **Contacts** tab has **Team** and **Address book**. Calls
  from or to a saved number show the contact's name.

---

## 5. The dialer (your browser is your phone)

### Start it

1. Click the **round phone button at the bottom right**. The dialer opens
   above it.
2. Click **Start the dialer** (or switch on the toggle at the top of the
   dialer).
3. The browser asks for the **microphone**. Click **Allow**.
4. Allow **notifications** too, so you see incoming calls while you work in
   another tab.
5. The dots turn green, and the status at the top of the dialer shows
   **Available · Ext 1001** (or **Ready · Ext 1001** if you are not an
   agent). Your extension is now registered from the browser, and the
   Dashboard lists it as a device called "Web Dialer".

The dialer rings **together with** your desk phone or softphone; answer on
either one. It stays on while the tab is open, even when you switch pages or
close the dialer window. When a call comes in, the dialer opens by itself
and the round button pulses green. Switching the toggle off (or closing the
tab) unregisters it.

### The dialer window

The dialer opens out of the round button at the bottom right and closes
back into it. **Drag it by its top bar** to put it anywhere in the page
(below the top bar, right of the menu): it stays there, also after a
reload, and the live transcript panel follows it. Double-click the top bar
to put it back in the corner. (On a phone it fills the screen and does not
move.)

- **Top**: the on/off toggle, and your **status** (e.g. "Available · Ext
  1001"). Click the status to change it: Available, a break (Lunch,
  Meeting…), Logged out, or **Do not disturb**. You can also stop the dialer
  there.
- **Keypad tab**: "From 1001" shows the number your calls come from. Type a
  number (or click the round keys; typing on your keyboard works too) and
  press the green button.
- **During a call**: the person's name and number, the call time, and round
  buttons: **Mute**, **Keypad**, **Hold**, **Transfer**, **Consult** (then
  **Complete transfer**) and **New call**. The red button hangs up. Other
  calls on hold are listed above, with **Resume**.
- **Bottom tabs**: **Recents**, **Voicemail** (with a red number for new
  messages), **Keypad**, **Contacts** (Team and Address book) and
  **Settings**.
- **Audio check** (under the call buttons): four lines, each with a dot and
  a level bar:
  - **Your mic → server**: what your microphone sends;
  - **Server → other side**: what the server passes on;
  - **Other side → server**: what the phone or carrier sends back;
  - **Server → your speaker**: what reaches this browser and plays.

  Green is fine. Amber "Quiet" or "Only silence" means sound arrives but
  nobody speaks, or a microphone is muted. Red "Nothing arrives" is where
  the sound stops; the box below says what to do. Round trip, buffer and
  lost packets are shown at the bottom. When the browser refuses to play
  the call (it can happen after a page reload), a blue **Turn on sound**
  button appears: click it.
- **Settings**: the dialer on/off switch, queue status, do not disturb, call
  notifications, and **Audio**:
  - **Microphone** and **Speaker**: choose your headset (the speaker can
    be chosen in browsers that support it);
  - **Microphone boost**: 50 % to 300 %, for a quiet microphone (the other
    side hears you louder);
  - **Your voice level**: a bar that moves when you speak;
  - **Speaker volume** and **Speaker test**;
  - **Audio path**: "UDP (best)", or "TCP" when UDP port 8443 is blocked
    somewhere. TCP works but adds delay: open UDP 8443 to fix it.

### What it does

| Button | What it does |
|---|---|
| **Call** | Type a number (extension, queue, outside number) and press Enter or Call |
| **Answer / Decline** | Incoming calls ring in the browser, with the caller's number and name |
| **Hold / Resume** | The other person hears music on hold |
| **Mute / Unmute** | Your microphone |
| **Keypad** | Sends digits during a call (IVR menus, voicemail PIN) |
| **Transfer** | Blind transfer: sends the call to another number straight away |
| **Consult** | Attended transfer: the caller waits on hold while you call the colleague; then **Complete transfer** connects them |
| **Several calls** | Answering a second call puts the first one on hold. Use **Hold / Resume** to switch between them. |
| **Contacts** | Every extension, with a live status (Available, On a call, Break, Do not disturb, Offline), and call and chat buttons. Search by name or number. |
| **Recents** | Your recent calls, with a phone button to call back |
| **Voicemail** | Your messages: play (marks them heard) and download. Delete them under **Communicate → Voicemail**. |
| **Queue status** | If you are an agent (click your status at the top): **Available**, **Logged out**, or a break with an AUX code (Lunch, Meeting…). This is the same status as `*22` / `*23`, the Call centre page and the Wallboard switch. |
| **Do not disturb** | Calls to you get busy, or voicemail if it is on |

### Needs

- A current desktop or mobile browser. The page must be opened with **https**.
- **UDP port 8443** open in the firewall (section 1, Step 2) for the best
  sound. If UDP is blocked, the audio goes over TCP on the web port
  instead.
- A headset is recommended. Laptop speakers cause echo.

If the dialer shows a red **No audio connection to the server** box, it
lists the address it tried. Open that UDP port, then click **Try again**.

---

## 6. The live dashboard: Listen, Whisper, Barge in, Divert, Hang up

**Dashboard** updates by itself the moment something changes.

- **Active Calls** (the big card): one line per call, with the counters
  **Active**, **Inbound** (calls from your trunks) and **Outbound** (calls
  out through a trunk). When nothing is going on it says "No calls in
  progress", with a **View call history** button.
- **Devices online** (Owners): every phone and browser dialer signed in.
- On the right:
  - **System Health** (Owners): CPU, memory and disk of the server, with a
    small trend line. A bar turns amber at 75 % and red at 90 %.
  - **Extensions**: how many there are, and how many are registered or
    unregistered.
  - **Trunks** (Owners): your gateways and whether they are connected. The
    card turns red when none is.
  - **System Information**: version, server, domain and uptime.
  - **Recent calls**: today's last calls, with a red "N failed" mark for
    missed, busy or failed calls.

Each line in **Active Calls** shows from, to, state (Ringing, Talking, In
queue…) and time. An arrow icon shows the direction. Owners get these
buttons on each call:

| Button | What happens |
|---|---|
| **Listen** | Your extension rings. Answer it to hear both sides; nobody hears you. |
| **Whisper** | You talk to your agent only; the caller does not hear you (coaching). |
| **Barge in** | You join the call; everybody hears everybody. |
| **Divert** | Sends the caller to another number (an extension, queue or outside number). The other side is disconnected. |
| **Hang up** | Ends the call. |

While listening, press **1** (listen), **2** (whisper) or **3** (barge in)
on your phone to switch mode. Hang up to stop. The call carries on
normally. Listen, Whisper and Barge in ring the extension you are signed in
with: your desk phone, or the browser dialer.

**Call centre (live)** shows each queue (waiting, longest wait, agents
available) and each agent (status, AUX code, for how long, current call).
Owners can set any agent's status; a User can set only their own.

---

## 7. Wallboard (TV)

**Wallboard** is a big-screen view of the call centre that updates live:

- **Tiles**:
  - **Call Waiting**: callers in a queue now; amber or red when too many
    wait.
  - **Active**: queue calls connected to an agent now.
  - **Total Calls**: today's queue calls.
  - **Abandoned** (red when above 0): callers who hung up while waiting.
  - **Agent Busy**: agents on a call now.
  - **Answered Calls %**, with the service level underneath (e.g. "Within
    20 s: 85%").
  - **Missed**: callers who waited too long and left the queue unanswered.
  - **Answered**, **Talk Time**, **Avg. Waiting**, **Avg. Handling**.
  - **Logged In**: agents logged in, out of all agents.
- **Queue Calls**: every caller in a queue, with queue, caller, agent,
  Waiting or Talking, and time. Owners get Listen, Whisper, Barge in and
  Divert buttons.
- **Agent Details**: every agent, with:
  - a **switch** to log them in or out (Owners can switch anyone; a User
    only themselves);
  - their extension, with a green dot when their phone is online;
  - state, reason (Available, Lunch, Offline…) and how long;
  - calls answered and missed today, the current call, and average talk
    time.
- **Queue Details**: one row per queue with its number and name, calls
  **answered**, **abandoned** and **missed** today, **% answered**, callers
  **waiting** now, **average waiting** and **talking** time, **agents** and
  **busy agents**.
- **Queue calls per hour today**: a chart of answered and abandoned calls,
  with a **Show as table** button.
- Each section has **−** (fold it), **+** (open it) and a **full screen**
  button; folded sections stay folded on that browser.
- **Settings**: choose the queues shown, when tiles turn amber or red, and
  the service level (e.g. 80 % answered within 20 seconds). Settings are
  saved in that browser.
- **TV mode**: full screen, without the menu. **Exit TV mode** goes back.

On a TV, sign in with a **User** extension made for the wallboard. Then open
the Wallboard and click **TV mode**.

---

## 8. Chat

**Communicate → Chat** (or **Chat** in the top bar).

- Click a person under **Start a chat** for a one-to-one chat (or **Chat** on
  a dialer contact). **+ New group** makes a group with a name and members;
  members can change it or leave.
- Type and press Enter. Shift+Enter starts a new line.
- **Files**: send with the 📎 button (up to 20 MB). Pictures show in the
  chat.
- You see who is typing, and who is online, on a call or on a break.
- Unread messages show a red number in the top bar and the menu. A browser
  notification appears when the tab is in the background.
- Messages are kept in the database, so you see them on any computer you
  sign in from.

Chat is between extensions of the same domain. The admin account has no
chat, because it is not an extension.

---

## 9. Meetings

Click the **camera button** in the top bar. The **Meet** window opens on
top of the page you are on. Its buttons (top right) **minimise** it (the
meeting goes on, in a small bar at the bottom left), **maximise** it, or
**close** it (you leave the meeting).

### Start or join

- **Left**: your picture (or your initials when the camera is off), with
  **Mic**, **Camera** and **Effects** under it, and **Devices and audio**
  to choose the microphone, camera and speaker. A level bar moves when you
  speak; if the microphone picks up nothing, a note says so.
- **Effects**: Blur background (when your camera or system offers it,
  e.g. the background blur built into your computer), Soft focus, Brighten, Warm,
  Black and white. Others see the effect too.
- **Start**: type a title (optional) and press **Start meeting**. **Meeting
  controls** sets the **waiting room** (guests ask to join and you let them
  in), whether **guests** may join, and a **passcode** for guests.
- **Join**: paste a meeting link or code (e.g. `46ak-c6kg-hvcz`) and press
  **Join**. Your planned meetings are listed below it.
- **Recent meeting content**: click **Load recent meeting content** to see
  your past meetings: when, how long, who came (with guests' emails), what
  was presented, and **Transcript** and **Chat** downloads. Owners and
  managers see all meetings of the company; others see the ones they were
  in.

**Communicate → Meetings** lists **planned** meetings (with a date and
time): **Plan a meeting**, **Join** (opens the Meet window), **Copy link**,
Edit and Delete. **Meet now** opens the Meet window.

### Guests (the meeting link)

The link looks like `https://wasip.ringq.io:8443/meet/m/46ak-c6kg-hvcz`.
It opens **Ready to join?**: the guest sees themselves, types their **name**
(and **email**, optional), and presses **Ask to join** (with a waiting
room) or **Join now**. While waiting they see "Waiting for the host to let
you in" and **Cancel and leave**. Colleagues signed in to the portal join
under their own name. Old links (`/meet.html#code`) still work.

### In the meeting

- **Top**: the meeting time, then **Copy link**, **Record**, **Transcribe**
  and **Present file**.
- **Bottom**: **Microphone** and **Camera** (the small arrows next to them
  choose the device, the speaker and effects), **Share screen**, **Raise
  hand**, **Reactions**, the **pen** (whiteboard), **Chat** and the red
  **Leave** button. On the right: **Settings**, **People** (with the number
  of people, and a red number for people waiting) and the **shield** (host
  controls).
- **Someone asks to join**: "Ravi wants to join" appears at the top right
  of the meeting with **Decline** and **Admit**. **People** lists everyone
  waiting (**Admit all**) and everyone in the meeting.
- **Transcribe**: everyone's speech is written down live (captions at the
  bottom) and kept as a transcript (shield → Transcript, or later in Recent
  meeting content). Each browser listens to its own microphone with its
  built-in speech recognition; people whose browser has none are just not
  transcribed. Browsers send the sound to their own speech service for
  this. Choose the language in **Settings**.
- **Present file**: choose a PDF or a picture; everyone sees it large. For
  a PDF, the presenter turns the pages with ‹ and ›. **Stop presenting**
  ends it. Files stay on the server only while the meeting runs.
- **Whiteboard** (the pen): everyone can draw, in five colours or with the
  eraser. **Clear** wipes it, **Close** closes it for everyone.
- **Record** records what you see and hear to a file on your computer
  (.webm, plays in any current browser). Tell people before you record.
- **The host** (who started or planned the meeting, or an Owner) can, under
  the shield: switch the **waiting room** on or off, **lock** the meeting,
  **mute everyone**, mute or **remove** one person, and **end the meeting
  for all**. When a host presses Leave, they choose between leaving and
  ending it for everyone.

### Needs

- **https**, and preferably **UDP port 8443** open (section 1, Step 2).
  Without UDP, audio and video go over TCP on the web port.
- Guests need nothing installed. A browser is enough, including on a phone.
- The server forwards each person's video to the others without
  re-encoding it, so it needs little CPU. Network traffic grows with the
  number of people.

---

## 10. Security settings

**Settings → Security** (or the **Security** tile of Global Settings). Owners
see their company's tabs (Allowed countries, PIN-based dialing, Webapp auto
logout); the server-wide tabs (web IP rules, Anti-hacking, SIP Guard) need
server access (the administrator, and owners unless `owner-server-access`
is off).

**Allowed countries.** Pick a continent (North America, South America,
Europe, Asia, Africa, Australia, and **Other**: satellite phones and global
networks, a common target of toll fraud). Every country is a card with its
flag and calling code; untick the ones your company never calls, or
**Select all** to tick or untick a whole continent, then **Save**. A call out
through a trunk to a number written with `+`, `00` (or `011`) whose country
is not ticked is refused: the caller hears "Sorry, calls to this country
are not allowed". Local numbers always go through. Kept in RingQ's
`q_security_restrictions.blocked_countries`.

**PIN-based dialing.** **No PIN**, **Numbers starting with** (e.g. `00`:
international calls; `+` counts as `00`) or **Every call out**. Tick the
**extensions that never need a PIN** (the boss, reception). Add the PINs
below (3 to 16 digits, an optional account code and a description; untick
**Enabled** to stop one). When a call needs a PIN, the caller hears "Please
enter your PIN, followed by the pound key" (three tries); with a valid PIN
the call goes out, and the PIN and its account code are kept with the call
(Call history, `v_xml_cdr.pin_number` and `accountcode`). Kept in RingQ's
`q_security_rules` and `v_pin_numbers`.

**Webapp & mobile restriction.**

- **IP**: Action **Deny** (that address or network cannot open the web
  interface, the browser dialer or the API) or **Allow** (once there is one
  Allow address, only Allow addresses may open it: add every office). The
  description shows in the list; tick rows to delete several at once.
  Rules that would lock you out are refused.
- **Webapp auto logout**: off, or 5 minutes to 8 hours. After that long
  without mouse, keyboard or touch, the page asks "Are you still there?" and
  signs out a minute later; nobody is signed out during a call. A session
  that sends nothing at all for that long (a closed laptop) ends too.

**Anti-hacking.**

- **Enable global IP restriction**: only the **Allow** addresses below,
  your trunks (gateways) and trusted carriers may send SIP. Phones
  registering from anywhere else stop working, so add your offices first.
  The browser dialer is not affected.
- **Allow / Deny list**: **Deny** drops SIP from that address or network;
  **Allow** is never banned by SIP Guard (your office, your phones).
- **Trusted carriers**: provider addresses allowed to send calls without
  signing in (the portal's "providers" ACL).
- **SIP scanners**: text in the User-Agent of attack tools (10 common tools
  are added for you); they are ignored and banned on their first packet.

**SIP Guard.** The bar shows how many protections are loaded and how many
addresses are banned; the switch on the right turns SIP Guard on and off
(off lifts every ban). Each protection bans an address that fails
**Failures** times within **Window**, for **Ban**:

| Protection | Counts | Default |
|---|---|---|
| SIP Request Protection | calls from outside (no sign-in, not a trunk) to numbers that do not exist: dial plan probing | 20 in 1 minute, 1 hour |
| SIP Registration Protection | failed sign-ins of unknown or disabled users | 30 in 5 minutes, 30 minutes |
| Web Request Protection | web pages that do not exist (vulnerability scanners), requests without signing in | 300 in 1 minute, 1 hour |
| Web Traffic Protection | every web request (denial of service) | 3000 in 1 minute, 1 hour |
| SIP Password Protection | an existing extension with a wrong password | from `auth-fail-threshold`, `auth-fail-window`, `auth-block-seconds` in ringqswitch.xml (10 in 1 minute, 10 minutes) |
| Admin Login Protection | failed sign-ins to this web interface | 10 in 10 minutes, 1 hour |
| SIP Invite Protection | too many calls from one address (toll fraud, call spam) | off; 200 in 3 minutes, 1 hour |

A SIP ban closes SIP to the address; a web ban closes the web interface.
Your trunks, trusted carriers, **Allow** addresses and this server are never
banned. The pen of a protection changes it (on/off, failures, window, ban).
**Remote Server Access Protection** (SSH) belongs to the server's own SSH
protection service: when one is installed its SSH settings are shown
(read-only; RingQswitch runs without root).

- **Live bans**: each protection with its banned addresses (the ⓘ shows why
  and until when). The bin unbans one; tick several and **Unban selected**.
  To ban by hand, choose the **Protection area** (which protection's ban
  time, and SIP or web), type the address and **Add**. Search by address or
  protection name.
- **History & stats**: bans in the period (24 hours to 90 days), in the last
  24 hours, different addresses, the most active protection; bans by
  protection; the most banned addresses; recent events (banned, unbanned,
  ban ended, by whom).

Bans survive restarts (RingQ's `sip_security_blocks`, history in
`security_audit_log`); `rq_cli -x "blocked"` lists them and `rq_cli -x
"unblock 203.0.113.7"` lifts them. Rules are in `rq_security_rules`, the
SIP Guard settings and the global IP restriction in `rq_settings`.

---

## 11. How the web server is set up (SSL)

### 11.1 The default

RingQswitch has its own built-in web server; you do not need another web
server. The settings are in the `<web>` section of
`/etc/ringqswitch/ringqswitch.xml`:

```xml
<web>
  <param name="listen" value="0.0.0.0:8443"/>   <!-- address:port of the web page -->
  <param name="tls" value="self-signed"/>       <!-- or "cert.pem,key.pem", or "off" -->
  <param name="admin-user" value="admin"/>
  <param name="admin-password" value="..."/>
  <param name="webrtc-port" value="8443"/>      <!-- UDP port for dialer/meeting audio and video -->
  <!-- <param name="public-ip" value="174.138.24.53"/>  only behind NAT -->
  <!-- <param name="owner-server-access" value="false"/> -->
</web>
```

- **self-signed** (what 0.3.0 used): RingQswitch makes its own certificate,
  so the browser warns once.
- **"cert,key"**: your own certificate. This is what `use-certificate.sh`
  sets up (section 1, Step 4):

  ```xml
  <param name="tls" value="/etc/ringqswitch/tls/wasip.ringq.io/fullchain.pem,/etc/ringqswitch/tls/wasip.ringq.io/privkey.pem"/>
  ```

### 11.2 Why the certificate is copied

The service runs as the user `ringq`, which cannot read
`/etc/letsencrypt/live/...` (only root can). The script copies the two files
to `/etc/ringqswitch/tls/wasip.ringq.io/`. The key there is readable by
`ringq` only. The originals are not changed.

Renewals take care of themselves. The certificate renewal runs the hook
`/etc/letsencrypt/renewal-hooks/deploy/ringqswitch-wasip.ringq.io.sh`, which
copies the new files, and RingQswitch loads them within a minute. To test
renewal: `sudo certbot renew --dry-run`.

### 11.3 Behind another web server (only if one already uses port 443)

To open the portal as https://wasip.ringq.io/ while another web server
(a reverse proxy) serves port 443, let it pass the portal through.

1. In `/etc/ringqswitch/ringqswitch.xml`, make RingQswitch listen on this
   server only, without TLS (the proxy does TLS):

   ```xml
   <param name="listen" value="127.0.0.1:8088"/>
   <param name="tls" value="off"/>
   ```

2. Add a site to the proxy, e.g. `/etc/nginx/sites-available/ringqswitch`.
   Link it into `sites-enabled` with
   `sudo ln -s /etc/nginx/sites-available/ringqswitch /etc/nginx/sites-enabled/`:

   ```
   server {
       listen 443 ssl http2;
       server_name wasip.ringq.io;
       ssl_certificate     /etc/letsencrypt/live/wasip.ringq.io/fullchain.pem;
       ssl_certificate_key /etc/letsencrypt/live/wasip.ringq.io/privkey.pem;
       client_max_body_size 60m;             # sound and chat file uploads

       location / {
           proxy_pass http://127.0.0.1:8088;
           proxy_http_version 1.1;
           proxy_set_header Host $host;
           proxy_set_header X-Real-IP $remote_addr;
           proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
           proxy_set_header X-Forwarded-Proto https;
           proxy_set_header Upgrade $http_upgrade;   # live updates, dialer, chat, meetings
           proxy_set_header Connection "upgrade";
           proxy_read_timeout 1h;
       }
   }
   ```

3. Check and reload the proxy, then restart RingQswitch:
   `sudo nginx -t && sudo systemctl reload nginx && sudo systemctl restart ringqswitch`.

RingQswitch believes the `X-Real-IP` header only from a proxy on the same
server. Failed-login protection and the web allow-list therefore see the
real browser address. **UDP 8443 must stay open**: audio and video for the
dialer and meetings go straight to RingQswitch, not through the proxy.
Behind a proxy the TCP fallback is not available, because the proxy owns the
web port.

---

## 12. Your first incoming call flow: Gateway → DID → Queue

Example: calls to your number 6531591234 arrive from your carrier and should
go to a "Support" queue answered by extensions 1001 and 1002.

The **Call Flow** page has the **Trunks** card (tabs **SIP Trunk**,
**Gateway**, **DID**) and **Available DID Numbers** on the left, and the
places a number can go on the right: **Interactive Voice Response**,
**Queue**, **Extension** and **Others**. **View 2** shows the same as one
table; **Customize** hides cards you do not use; the arrows on a card make
it fill the page.

### 12.1 Add the trunk

**Call Flow → Trunks → SIP Trunk → +** (a provider that gave you a username
and password), or **Gateway → +** (a provider that trusts your server's IP
address). A window opens:

| Field | What to enter |
|---|---|
| Name | Any name, e.g. `MyCarrier` |
| Provider address | The SIP server your provider gave you, e.g. `sip.mycarrier.com` or `203.0.113.10:5060` |
| Username / Password | From your provider (SIP Trunk only) |

The switch in **Enable** turns a trunk on and off; the pencil changes it and
the bin deletes it. The list shows the **Status**: **Connected**, **Ready (no sign-in)** for an
IP-authorised trunk, or **Failed** with the reason. An IP-authorised trunk
needs your carrier's IPs under **Settings → Security → Trusted carriers**.

### 12.2 Create the queue and pick its agents

**Contact center → Queues → + Add queue**

| Field | Example |
|---|---|
| Name | `Support` |
| Number | `3000` |
| Which agent gets the call | the strategy (see the table below) |
| Music while waiting | Default music, or a folder you uploaded |
| Then go to | where callers go if nobody answers in time, e.g. `Voicemail 1003` |
| Agents in this queue | **+ Add an agent**, then **choose the extension** (1001, 1002) from the list |

Choosing an extension makes it an agent by itself. The defaults:

- ring for 20 seconds;
- 10 seconds of wrap-up after each call;
- an automatic break after 3 missed calls in a row.

To change these for one person, open the extension and go to
**Call-centre agent**. A separate Agents page is no longer needed.

**When does a queue call go to voicemail?** A caller waits in the queue
(with the music) until an agent answers, or until one of these happens;
then they hear a short message and go to the queue's **Then go to** (set
it to an extension's voicemail; empty: the call ends):

| What happens | Setting (queue) | The caller hears |
|---|---|---|
| Nobody can answer for a while: every agent is on a break, logged out, or has no phone online | **Leave when no agent is available for (s)** (default 90; *…after waiting at least* 5 s) | "Sorry, no agent is available to take your call at the moment." |
| The caller has waited too long, even though agents are there but busy | **Longest wait (seconds, 0 = no limit)** | "Sorry, all our agents are still busy." |
| The caller presses the exit key | **Exit key** | (nothing) |

Agents who do not answer: each call rings an agent for their **ring time**
(20 s); a call they miss goes to the next agent (or the same one again
after a pause). After **3 missed calls in a row** the agent is put on a
break, "Max no answer", and gets no queue calls until they make themselves
Available again (dialer, `*23`, wallboard switch). Then, if nobody else
is available, the rule above sends callers on after 90 s. For callers to
reach voicemail sooner, lower "Leave when no agent is available for", or
set a "Longest wait" (for example 60 s). Agents with no phone online are
skipped: they are never rung and collect no missed calls.

| Strategy | Who gets the next call |
|---|---|
| ring-all | Every free agent rings at once; the first to answer gets it |
| longest-idle-agent | The agent who has waited longest since their last call (fair; the usual choice) |
| round-robin | Agents take turns |
| top-down | Always the first agent in the list first, then the next |
| agent-with-least-talk-time | The agent with the least talk time today |
| agent-with-fewest-calls | The agent who answered the fewest calls today |
| sequentially-by-agent-order | One at a time in list order |
| random | A random free agent |
| ring-progressively | The first agent rings, then one more agent is added every few seconds |

### 12.3 Add the DID (your phone number) and drag it to the queue

1. **Call Flow → Trunks → DID → +**. In the window:
   - **Country**: e.g. **Singapore (+65)** (typing a full number such as
     `6531591234` picks it by itself). Calls then match the number with or
     without the country code: `+6531591234`, `6531591234` and `31591234`;
   - **DID number**: as the provider sends it, e.g. `6531591234`;
   - **Trunk**: `MyCarrier`.
   Click **Save**. The number appears in the DID list (with its country
   flag) and under **Available DID Numbers**.
2. **Drag the number** from Available DID Numbers onto the **Queue** card.
   A window lists your queues: choose **3000 Support** and click **Assign**.
   The number now shows in the Queue card ("6531591234 → 3000 Support").
3. The same works for **Interactive Voice Response** (choose the IVR),
   **Extension** (choose the extension) and **Others** (ring group,
   conference room, office hours, voicemail, hang up, busy signal). You can
   also click a number instead of dragging it: a window asks where it goes.
4. To move a number, drag it from one card to another. To unlink it, **drag
   it back onto Available DID Numbers** (or click the **bin** on the
   number) and click **Move back** in the window: it goes back to
   Available DID Numbers, and calls to it are not answered until you link
   it again.

There is no fax module in RingQswitch, so Others has no fax entry.

### 12.4 Make the agents Available

**Agents start as Logged out, so they get no queue calls** (and the Dashboard
shows "Agents available: 0"). To make an agent Available, use one of:

- **sign in to the portal** with the extension: its agent is logged in
  (Available) at once, and signing out of the portal logs it out again;
- **the dialer**: click your status at the top of the dialer → **Available**;
- **the Wallboard**: switch the agent on in **Agent Details**;
- **the phone**: dial **`*22`** (rising tone = logged in);
- **the web**: **Call centre (live)** → **Available** next to the agent.

### 12.5 Test it

Call 6531591234 from your mobile. You hear music and Alice's or Bob's phone
rings. Watch the **Dashboard**, **Call centre (live)** and the **Wallboard**
while it happens.

---

## 12A. Hotel front desk: check-in, check-out and wake-up calls

Menu **Hotel front desk** (owners, managers and the administrator). Every
extension is a room: the room number is the extension number. The tabs
show **All rooms**, **Occupied** and **Vacant** with their counts; the
search box finds a room number or a guest's name. A green line and dot is a
vacant room, orange an occupied one; a small bell shows a planned wake-up
call.

Click a room: its panel opens on the right.

- **Check in a guest**: the window shows the room (read-only), then
  **Guest first name** and **Guest last name**. Click **Check in**. The room
  turns Occupied. A room that is occupied cannot be checked in again until
  the guest checks out.
- **Edit guest** changes the name.
- **Check out** ends the stay (the room turns Vacant) and cancels the
  room's planned wake-up call.
- **Schedule wake-up** (only with a guest in the room): choose the **date**
  and **time** (it offers 07:00 the next morning) and the **wake-up sound**
  (beeps, or one of your prompts from Sounds & music). At that time the
  room's phone rings with the caller ID "Wake-up call" and plays the sound
  when answered. If nobody answers, it rings again 5 minutes later, 3
  tries in all. The panel shows **Scheduled**, **Ringing the room now**,
  then **Answered** or **Not answered**. **Change** moves it, **Cancel
  wake-up** cancels it; a room has one planned wake-up call at a time.
- **Phone** shows whether the room's phone is registered.

Each wake-up call is written to **Settings → Event log** (kind "hotel").
The times are your browser's local time.

---

## 12B. Single sign-on: Microsoft, Google, LinkedIn

People can sign in to the portal with their work account instead of the
web password. **Settings → Integrations** (owners) shows a card per
provider; each opens a window with the steps, the **Redirect URI** to copy
and the fields to fill in. Each domain sets up its own providers.

**Microsoft**

1. In Microsoft's admin center: **App registrations → New
   registration**. Redirect URI: platform **Web**, paste the Redirect URI
   from the RingQswitch window (it ends in `/api/sso/callback/microsoft`).
2. Copy the **Application (client) ID** and the **Directory (tenant) ID**.
3. **Certificates & secrets → New client secret**: copy its **Value**.
4. In RingQswitch: **Microsoft → Configure**, paste the three values, keep
   **Enable SSO** on, **Save configuration**. A wrong tenant ID is reported
   at once.

**Google**: Google's cloud console → APIs & Services →
Credentials → **Create credentials → OAuth client ID → Web application**;
add the Redirect URI; copy the client ID and secret into **Google →
Configure**.

**LinkedIn**: LinkedIn's developer portal → create an app → Products: **Sign
In with LinkedIn using OpenID Connect** → Auth tab: add the Redirect URI;
copy the client ID and secret into **LinkedIn → Connect**.

**Who is signed in.** The account's email chooses the extension:

1. the extension's **Sign-in email (single sign-on)** (extension page, under
   Web portal access), else
2. the email of the RingQ user linked to the extension, else
3. the extension's **Email address** (its voicemail email).

The extension needs web access (User, Manager or Owner), like a password
sign-in. An email used by two extensions is refused (set a sign-in email on
one of them).

**The sign-in page** then shows **Sign in with Microsoft** (Google,
LinkedIn) under the password form. If several domains use the same
provider and the address in the browser is not one of them, it asks for the
company domain first.

**Sign-in options → Web password**: turn it off (and **Save**) so that the
domain's extensions can only sign in with a provider. This needs a provider
turned on; removing the last provider turns passwords back on. The
administrator account always signs in with its password.

The client secret is kept encrypted with the key in the data directory
(`web-passwords.key`). Every SSO sign-in and refusal is in the event log
(kind "security").

## 12C. eFax: sending and receiving faxes

Menu **eFax** (owners, managers and the administrator). Faxes travel as
audio on the call (G.711), the way the RingQ fax engine sends them, so any
SIP trunk that carries calls carries faxes; when the carrier switches the
call to T.38 (its fax gateway asks with a re-INVITE), the fax goes on
over T.38.

**Tabs**

- **Overview**: faxes sent, received, in progress and failed, today's
  count, and the latest faxes.
- **Send eFax**: choose **From** (one of your DID numbers; the fax goes out
  on that number's trunk with it as caller ID; a DID without a trunk goes
  by the outbound rules), type **To** (several numbers separated by commas,
  20 at most), drop the **document** (PDF, TIFF, PNG, JPEG or GIF, 20 MB at
  most) and click **Send eFax**. The document is checked at once (a file
  that cannot be faxed is refused before anything is dialled) and the fax
  is queued; up to 4 faxes go out at the same time.
- **Inbox**: the faxes received, newest first. The eye opens the fax (a
  PDF), the arrow downloads it.
- **Status log**: every fax with its status: **Queued**, **Dialling**,
  **Sending** (with the page being sent), then **Sent** or **Failed** (in:
  **Receiving**, then **Received** or **Failed**). A failed fax shows why
  (Busy, No answer, The number is not in service, the fax machine's own
  reason...). **Send again** (the circular arrow) queues a failed fax
  again; the bin deletes a fax and its file.
- **Settings**: the **Station id** (shown on the receiving machine, 20
  characters) and the **Page header** printed at the top of every page with
  the date, your number and "P.1/3". Empty, both are the company name
  (RingQ's company record), else "Fax".

**Receiving faxes on a number**

1. **eFax → Settings → Manage fax extensions** (or Call Flow → Others →
   **+** → Fax extension): **New fax extension**: a name (e.g. Office fax)
   and a number (e.g. 3290). **Fax number (station id)** is what the
   sender's machine shows (your fax DID, e.g. 6531591234).
2. **Call Flow**: drag the fax DID onto **Others** and choose **Fax 3290
   Office fax** (or open the DID and set "Send calls to" to it).
3. A fax sent to that number is answered as a fax machine and arrives in
   the **Inbox** as a PDF.

Without a fax extension: drag the DID onto **Others** and choose an
extension marked **eFax** (e.g. **eFax 1000 Ext 1000**). The number then
answers as a fax machine for extension 1000 (the dialplan below, with
`fax_extension=1000`), and shows in the Others card as "→ eFax · 1000 Ext
1000". An extension is for calls when the DID is dropped on **Extension**,
for faxes when it is chosen under **Others**.

**The dialplan of a fax DID** (Settings → Dialplans shows it) is the one
RingQ's fax DIDs use:

```xml
<extension name="6531591234" continue="false" uuid="...">
  <condition field="${destination_number}" expression="^((\+)?6531591234)$">
    <action application="set" data="fax_direction=in"/>
    <action application="set" data="domain_uuid=..."/>
    <action application="set" data="fax_verbose=true"/>
    <action application="set" data="fax_enable_t38=true"/>
    <action application="set" data="fax_enable_t38_request=false"/>
    <action application="set" data="fax_enable_ecm=true"/>
    <action application="set" data="ignore_early_media=true"/>
    <action application="set" data="fax_file=${fax_dir}/<domain uuid>/inbox/${uuid}.pdf"/>
    <action application="answer" data=""/>
    <action application="stop_record_session" data="${record_path}/${record_name}"/>
    <action application="rxfax" data="${fax_file}"/>
    <action application="hangup" data=""/>
  </condition>
</extension>
```

(plus `caller_destination`, `fax_uuid`, `fax_extension` and `fax_ident`
for the eFax inbox). `fax_enable_t38=true` lets the carrier switch the fax
to T.38 (it is also what happens without the variable);
`fax_enable_t38=false` keeps it audio. RingQswitch does not ask for T.38
itself, and has no error correction mode: `fax_enable_t38_request` and
`fax_enable_ecm` are accepted for existing dialplans and change nothing here.

**Where the files are** (`fax-dir`, by default `/var/lib/ringqswitch/fax`):

| | Folder | File |
|---|---|---|
| Received | `<fax-dir>/<domain uuid>/inbox/` | `<call uuid>.pdf` |
| Sent (the document you uploaded) | `<fax-dir>/<domain uuid>/outbox/` | `<fax id>.pdf` (or `.tif`, `.png`, `.jpg`, `.gif` as uploaded) |

To keep them where the RingQ fax engine did, set `<param name="fax-dir"
value="/var/spool/fax"/>` in `/etc/ringqswitch/ringqswitch.xml`, run
`sudo mkdir -p /var/spool/fax && sudo chown ringq:ringq /var/spool/fax`
and restart.

**Good to know**

- Sending a **PDF** needs the PDF converter on the server: `sudo apt install
  ghostscript`. TIFF (including the Group 4 TIFFs the RingQ fax engine
  makes) and images send without it. The eFax page warns when it is
  missing.
- Modems: V.29 (9600 and 7200 bit/s), standard and fine resolution, no
  error correction mode. Nearly every fax machine and fax service speaks
  V.29; a machine that only does V.27ter (4800) is not supported yet.
- Calls with a fax use G.711 (PCMU/PCMA). A trunk that asks to switch to
  T.38 gets it (version 0 to 3, with redundancy against lost packets),
  unless `fax_enable_t38=false`. The fax starts over on T.38 at that
  moment, which is how carriers expect it (they switch on the first fax
  tones).
- If you ran the RingQ fax engine (`ringq-fax-worker`) against this
  database, stop it: RingQswitch now sends the queued faxes itself (both
  use the same `fax_jobs` table, so the history stays).
- Every fax is written to **Settings → Event log** (kind "fax").

**When a fax fails**

1. Follow it live: `rq_cli`, then send the fax again. Each fax prints lines
   like:

```
fax: hearing 247 packets, level -12.3 dBm0, 2100 Hz (CED)
fax: got CSI DIS
fax: send TSI DCS
fax: send TCF at 9600
fax: got CFR
fax: send page 1 at 9600 (99291 bits)
fax: V.21 signals heard 3, messages received 4, damaged 0
```

   On a call the carrier switches to T.38:

```
fax: the far end switched the call to T.38: the fax goes on over UDPTL version=0 error_correction=t38UDPRedundancy
fax: T.38: 43 packets received in 5 s (55 in all), 74 sent
fax: V.21 signals heard 3, messages received 4, damaged 0; T.38 datagrams received 79 (unreadable 0), sent 448, packets lost 0, recovered 0
```

   - "no audio is arriving from the far end": RTP from the trunk does not
     reach RingQswitch (firewall, NAT, the trunk's media address).
   - Audio but "no fax tones": the number answered as a voice line (or a
     menu), not a fax machine.
   - CED or V.21 heard, but "messages received 0" and many "damaged": the
     fax signal arrives damaged (a compressed codec in the carrier's
     network, packet loss).
   - "T.38: no packets are arriving from the far end": the carrier
     switched to T.38 but its packets do not reach RingQswitch (firewall:
     open the RTP port range for UDP from the carrier's media servers).
     "the far end is quiet" while a page goes out is normal.
   - "re-INVITE to T.38 refused": the call has `fax_enable_t38=false` (or
     no fax was running); the fax stays audio.
2. The call's audio of a failed fax is kept for 7 days:
   `<fax-dir>/<domain uuid>/trace/<call uuid>-heard.wav` and `-sent.wav`,
   also downloadable from the Status log (ear and speaker buttons). Send
   the "heard" file to your developers: it shows exactly what the far end
   sent. Set `fax_trace=true` on a call to keep its audio even when it
   works.

## 12D. Call transcription and sentiment

Menu **Settings → Call transcription** (owners). After each call,
RingQswitch writes down who said what and when, and how they felt. It all
happens on your server: the speech recogniser understands 99 languages and
no call audio is sent anywhere.

**Install the speech recogniser once** (about 5 minutes; it downloads and
builds the recogniser and a speech model):

```bash
sudo bash /usr/local/share/ringqswitch/install-transcription.sh
```

(`sudo make install` puts it there; it is also `packaging/install-transcription.sh`
in the source folder.)

It picks the **small** model (466 MB, more accurate) on a server with 4 or
more CPUs and 4 GB of memory, **base** (142 MB, faster) otherwise; choose
with `--model base`, `--model small` or `--model medium` (1.5 GB, most
accurate, needs a fast CPU). It ends by transcribing a short English
sample and printing what it heard. No restart: within a minute the
settings page shows **Ready**.

**More models**: the **Speech models** list on the same page has every
speech model with its size and speed: Tiny, Base, Small, Medium,
Large v3 Turbo (also a compact one), Large v3, and English-only versions
of the smaller ones. The administrator (or an owner allowed the server
pages) clicks **Download**: the server fetches it from the speech model
library on the internet (`transcribe-model-url`, so the server needs
internet access) into `/var/lib/ringqswitch/speech/`, with a progress bar. **Delete** removes a
downloaded model; one installed by the script is removed on the server
(`sudo rm` on the path shown). Then choose the model each domain uses
under **Speech model** (*Server default* is Small, or Base, whichever is
installed, or the one set with `transcribe-model`). Larger models
understand accents, noisy lines and other languages better but take
longer per call: Medium about three times as long as Small; the Large
models are slower still on a server without a graphics card.

**Turn it on**: Settings → Call transcription → **Transcribe calls** →
Save.

- **Which calls**: *Incoming and outgoing calls* (through a trunk: the
  default) or *All calls*, including calls between extensions.
- **Language spoken**: *Detect automatically* works for most calls (each
  speaker's language is found on their longest turn, then used for their
  other turns: "Hello?" alone is easily taken for another language);
  choose the language when calls are mostly in one (English, Malay, Tamil,
  Chinese, Hindi...). Sentiment is scored for English.
- **Speech model**: see above.

**How it works**

1. A call to transcribe is recorded from the moment it is answered, in
   stereo: Speaker A (the caller) on the left, Speaker B (who answered:
   the extension, agent, or the number dialled) on the right. That is how
   the transcript knows who spoke; nothing is guessed. A call your
   dialplan already records (a DID's **Record calls**, `record_session`)
   is recorded the same way. Recordings go where RingQ keeps them
   (`<recordings-dir>/<domain>/archive/<year>/<month>/<day>/<call uuid>.wav`)
   and play from Call history as before. Calls are only recorded for a
   transcript while the recogniser is installed.
2. After the hang-up the call waits in line (a clock in the
   **Transcript** column) and is transcribed, one call at a time by
   default. Each side is cut into **turns** (what a speaker says until the
   other one speaks) and each turn is recognised on its own, so the lines
   keep their order: an answer cannot come before its question, whatever
   the recogniser's timing. Silence, and steady tones and hums, are left
   out. It runs at low CPU priority, so calls in progress are not
   affected. How long it takes depends on the CPU, the model and the
   number of turns (base is about three times faster than small); the log
   shows it for every call (`call transcribed ... took=`). Calls shorter
   than 5 seconds of talk time are skipped.
3. The **Transcript** column then shows the transcript icon; the report
   refreshes by itself.

**The transcript window** (click the icon):

- Cards for the **whole call**, **Speaker A** and **Speaker B**: the
  sentiment (Positive, Neutral, Negative, and the score from -1 to +1 on
  a red-to-green bar), talk time, how the speaker **ended** (the last
  third of what they said: did the customer finish happier?), and how
  many of their lines were positive and negative. On calls through a
  trunk the outside party is tagged **Customer** and the other **Agent**.
- The **recording** with a player, and the **conversation**: Speaker A on
  the left, Speaker B on the right, each line with its time and a
  sentiment dot. Click a line to play from there; the line being played
  is highlighted.
- **Copy** and **Download** (a .txt file), and, for managers and owners,
  **Transcribe again** (with the model chosen now: useful after changing
  the model, or for calls transcribed by 0.6.0).
- Esc or × closes it.

**In the reports**: **Call history** and **Queue calls** have a
**Transcript** column (icon: open it; clock: waiting or transcribing;
*No speech*; *Failed*, with a retry button in Call history for
managers and owners) and a
**Sentiment** column (the customer's sentiment when the call has a
customer, otherwise the whole call's). Export CSV includes both. In Call
history, managers and owners can **Transcribe** any recorded call
afterwards, also calls from before transcription was turned on. Users see the transcripts of
their own calls only.

**Good to know**

- Tell callers that calls are recorded (an announcement on your DIDs),
  as the law requires in most countries.
- Transcripts are stored in the database (`rq_transcripts`) and stay
  after the recording is deleted.
- Sentiment is a measure of the words used (a sentiment word list, with
  call-centre words added: "overcharged", "refund", "escalate"...), not of the tone of
  voice. "Sorry" said by an agent counts as neutral.
- More calls at once: `transcribe-workers` in ringqswitch.xml (each uses
  `transcribe-threads` CPUs, default half the CPUs, at most 4).
- The settings page lists the last 24 hours: waiting, transcribing,
  transcribed, no speech and failed, updated as calls are transcribed. A
  failure is logged with the reason (`journalctl -u ringqswitch | grep
  transcri`).
- **Voicemail messages** are transcribed too (switch **Transcribe
  voicemail messages**), one speaker, with the same model and language;
  see section 17.
- Testing with both phones in the same room: the other phone's
  microphone hears you too, so a word can show on both sides (the
  recording really holds it twice). Real calls do not have this.

### Live transcription

While a call goes on, the extension on it sees the conversation written
down as it happens, split by speaker.

**Turn it on**: Settings → Call transcription → **Live transcription
during calls** → Save. It does not need **Transcribe calls** (nothing is
recorded for it), only the speech recogniser. **Live transcription
model**: *Fastest installed* uses Base when it is installed (else Small,
then Tiny...); Base keeps up on most servers, Small is more accurate but
needs a fast CPU.

**Install the live recogniser** (once): the install script installs it
next to the recogniser since 0.7.0 (and, since 0.7.1, downloads the Base
model for live text when its main model is larger); on a server set up
earlier run it again:

```bash
sudo bash /usr/local/share/ringqswitch/install-transcription.sh
```

Without the live recogniser live transcription still works, a few seconds
later (each sentence loads the model again). With it, RingQswitch starts
the live recogniser by itself on 127.0.0.1 as soon as live transcription is on
for a domain, loads the model and keeps it loaded, so calls never wait
for it; it stops it when live transcription is turned off
(`transcribe-live-url` in ringqswitch.xml uses one started elsewhere
instead). The settings page shows *Live transcription: Running* once the
model is loaded (*Starting* while it loads, or why it could not start).
When the live model is Small or larger, the page suggests Base (about 3
times faster for live text), with a Download button.

**Language**: with *Language spoken* set to a language, live text is
recognised in it from the first word, and words so far show while people
speak. With *Detect automatically*, the call's language is fixed by its
first clear sentence (for both people on the call), and words so far show
from then on; what is heard before that with an unsure language is not
shown. Choose the language when your calls are mostly in one.

**What is left out**: the recogniser scores what it writes; text it
counts as a failed guess, guesses over noise, a word or two it doubts,
video-caption phrases it learnt ("Thanks for watching!") and words
repeated in a loop (cut short with "…") are not shown. Steady sounds (a
noisy line's background, a phone vibrating next to the microphone) are
not taken for speech at all: speech rises and falls with its syllables,
they do not.

**How fast**: each sentence is recognised on its own length (not
the recogniser's whole 30-second window) and without retries: on a 4-core
server a 3-second sentence takes about 0.1 s with Base and 0.3 s with
Small (before 0.7.1: about 0.7 s and 2 s, so with Small both people's
sentences queued up). A sentence ends after 0.6 s of silence (or at 8 s);
while someone speaks, their words so far are recognised about every
second when the recogniser is free.

**On a call**: when the call is answered, the **Live transcript** panel
slides out beside the dialer, as tall as it: on its left, or on its right
when the dialer was moved to the left edge (in the dialer's place when the
dialer is closed; over the page on a phone):

- The other party's lines on the left, yours on the right (**You**),
  each with the time into the call. Three dots show who is talking, with
  their words so far as they speak; when they pause the whole sentence
  is written in (the rest typed out).
- **LIVE** while the call goes on, **Call ended** after; the clock shows
  the call's time.
- **Copy** puts the conversation on the clipboard ("[0:05] You: ...").
  It is not saved: the transcript after the call (if **Transcribe calls**
  is on) is the one kept.
- **×** closes the panel for this call; the **Transcript** tab beside the
  dialer brings it back, with the number of lines said meanwhile. The
  next call opens it again.
- The panel shows on every open page of the extension, also when the
  call is on a desk phone or app (the dialer need not be started).
  A page opened (or reloaded) during the call gets what was said so far.

**Good to know**

- Each call being written down uses about one CPU core while people
  talk (Base model). The live recogniser runs at a lower priority than calls
  and a higher one than the transcripts made after calls.
- Only the extensions on the call see it; supervisors listening in do
  not get the panel.
- What is shown is written by the recogniser: names and numbers can be
  misheard. Short words alone ("yes", "ok") come out once the speaker
  has said a little more.

---

## 12E. AI agents: a voice AI that answers your calls

Menu **AI agents** (owners). An AI agent is a number, like an extension,
that answers calls itself. The caller hears your greeting and then talks
normally; the agent answers within a moment, in a natural voice, and can be
interrupted like a person (it stops talking as soon as the caller speaks).
It answers questions about your company and products from what you give it,
and other questions too, briefly. When the caller asks for a person it
connects the call.

The voice comes from a cloud **voice AI service** that works in real time
(speech in, speech out, no separate "recognise, then think, then read
aloud" steps, which is why it answers without a pause). This needs:

- an **API key** for the voice AI service (from your account on the
  service's developer platform → API keys) with credit: the service
  charges per minute of audio;
- **internet access from the server** to the service (port 443; Settings
  → AI agent service shows the default service address).

Calls to an AI agent are the only calls whose audio leaves your server
(to the voice AI service). Everything else (call transcription included)
stays local.

### Set it up (5 minutes)

1. **Settings → AI agent service**: paste the API key, **Save**, then
   **Test connection** (it should say *Connected*). Leave *Model* and
   *Service address* empty (the service's default voice model and address).
2. **AI agents → New AI agent**. On **Settings** give it a name and a
   number (e.g. 5000). On **Voice** choose the languages, a voice
   (**Preview** plays it saying your greeting) and the greetings; write the
   **Prompt** (who the agent is and how it helps) and the **Context** (facts
   it must always know: company, products, hours, address). **Save**.
3. **Call Flow**: drag your DID onto the **Interactive Voice Response**
   card and choose the agent (marked **AI**). Or, in an IVR menu, set a key
   to go to the agent.
4. Call the DID. The agent greets you; ask it something.

### The agent's page

**Voice**

| Field | What it does |
|---|---|
| Languages | The languages the agent speaks. It answers in the caller's language when it is one of them, otherwise in the first one. |
| Agent voice | Marin and Cedar sound the most like a real person; **Preview** lets you hear any voice saying your first greeting. |
| Noise field | *Near*: callers on a handset or headset (most calls). *Far*: callers on a speakerphone or in a car. It filters the background noise accordingly. |
| Connect keywords | A word the caller may ask for ("agent", "sales", "support") and where the call then goes (an extension, queue, ring group...). The agent says it is connecting them and transfers the call. If the caller says the word and the agent does not act on it, the call is connected anyway. Pressing **0** connects to the first keyword's destination. |
| Custom greetings | What callers hear first, word for word. With several, each call gets one of them. |
| Prompt | Who the agent is and how it behaves ("You are the friendly assistant of RingQ. Help callers choose a plan..."). |
| Context | Facts the agent should always know. |

**Knowledge**

- **Web pages**: up to 10 addresses (one per line). When you save, the
  server reads each page and the pages it links to on the same site (up to
  40 pages, two links deep; robots.txt is respected), leaving out menus and
  footers repeated on every page. **Pages read** lists them; **Fetch now**
  reads them again after your site changes.
- **Questions and answers**: anything callers ask, in your own words; leave
  a blank line between topics.

A small amount of knowledge is always in the agent's mind. With more (a big
website), the agent looks up the right part the moment the caller asks
(the search runs on your server, in a few milliseconds). It never invents
prices or facts: when it does not know, it says so and offers a person.

**Settings**: name, number, on/off, the longest call (the agent says
goodbye when it is reached), whether the agent may hang up after saying
goodbye, and **When the AI service cannot be reached, go to** (e.g. the
reception extension: used when there is no API key, the internet is down or
the service refuses the key).

**Status & log**: calls now, today and in the last 30 days, how many were
connected to people, the average length and failures; then every call
(caller, number called, length, result: *Completed*, *Transferred*,
*Failed*, *Interrupted* by a restart) with **what was said**, as a
conversation.

In **Call history** the call's path (the arrow on a row) shows *AI agent
... answered* and *AI agent connected the caller to sales (1000)*.

### Good to know

- The agent hears the call as the phone network sends it (8 kHz); a clear
  line gives the best results.
- Keep greetings short: callers can talk over them.
- The prompt and context work best as plain sentences; tell the agent what
  it must not do ("Do not promise discounts") as well.
- The API key is encrypted with the server's key file
  (`data-dir/web-passwords.key`). If that file is lost, enter the key again.
  Instead of saving a key, you can also set `RQ_AI_API_KEY` in the
  service's environment (`systemctl edit ringqswitch`).

---

## 13. Agent breaks (AUX codes)

| Status | Gets queue calls? |
|---|---|
| **Available** | Yes |
| **On Break** + a reason (AUX code: Lunch, Meeting…) | **No** |
| **Logged out** | **No** |

How an agent goes on a break:

- **Dialer**: **Queue status** → choose the AUX code (Break, Lunch, Meeting…).
- **Phone**: dial **`*23`** for a break (falling tone). Dial `*23` again to
  come back. `*23` uses the AUX code "Break".
- **Web**: on **Call centre (live)**, choose the reason in the **Break…**
  list next to the agent.
- **Automatically**: after too many missed calls in a row ("Max no answer").
- **RingQ over the event socket**:
  `callcenter_config agent set status <agent> 'On Break' Lunch` (API.md).

**AUX codes** are managed under **Contact center → AUX / break codes**. Tick
**Block direct calls** for codes like Meeting or Training: then the agent's
own extension does not ring either.

Every change is recorded with who, when, the old and new status, and how
long the old one lasted. You find it here:

- **Call centre (live)**;
- the **Wallboard**;
- **Settings → Event log** → *Agent status* (server administrators);
- **Reports → Agent status log** and **Agent status times**;
- the database tables `rq_agent_status_log` and `q_auxcodes_list`.

---

## 14. Other incoming destinations

- **IVR menu** ("Press 1 for sales…"): **Accounts → IVR menus**.
  1. Choose a greeting (upload it under **Settings → Sounds & music**).
  2. Add one row per key, e.g. key `1` → Queue 3000.
  3. Point a DID at the IVR.
- **Time conditions** (Accounts → Time conditions): a number with its own
  open days and times, where calls go when open and when closed. Holidays
  count as closed. The company-wide hours are on Language & office hours
  (section 14A), which comes first.
- **Ring group**: several phones ring for one number.
- **Extension**: one phone. Voicemail, forwarding and do-not-disturb are set
  on the extension.
- **Conference room**: callers who dial the room number talk together
  (optional PIN; 0 mutes yourself, # leaves). For video, use Meetings
  (section 9).

---

## 14A. Language & office hours

**Settings → Language & office hours** (or the tile in Global Settings).
Changes are saved as you make them ("All changes saved" at the top).

**Language & timezone.**

- **Language**: the voice prompts callers hear. English is built in; other
  languages appear when their sound pack is installed in the standard
  sound pack layout (`sounds/fr/ca/june/...`).
- **Timezone**: your company's time zone (default: the server's). Office
  hours, breaks, holidays and time conditions follow it, so one server can
  host companies in several countries. **Current time** shows the time
  there now.

**Global office hours.** One card per day, Monday to Sunday: green **Open**
(open all day), blue hours (e.g. 9:00 AM to 6:01 PM) or red **Closed**.
Click a day to change it (tick **The same for every day** to apply it to the
whole week). **Non-office hours action**: what happens to calls from outside
when you are closed: **None (calls go through)**, Hang up, Busy signal, or
any destination (voicemail, an IVR menu, a queue, a mobile through an
extension...).

**Global break hours.** The same, for breaks (e.g. lunch 12:00 PM to 1:01
PM); **No break** on days without one. **Break time action**: what happens
to calls during a break.

**Global holidays.** **Create holiday**: a name, the date (and an end date
for several days), **Every year on this date**, **All day** or from–to, and
the greeting (empty: the holiday sound default). On a holiday, callers hear
the greeting and the call ends. **Holiday sound default**: choose a sound or
**Upload** one (MP3 or WAV, up to 10 MB; converted for the phone network).
With no sound at all, callers hear "Thank you for calling. We are closed
today for a holiday...".

**Exempt from holidays.** IVR menus, queues and agents that stay open on
holidays (e.g. a support queue): calls to them go through as usual.

If the RingQ portal limited office hours, breaks or a holiday to some
numbers (its `work_extensions`, `break_extensions`, `special_extensions`,
a holiday's `extensions`), they apply only to calls to those numbers, and
the page says so under the section.

Order: a holiday first, then a break, then office hours. This applies to
calls coming in on your DIDs, before they reach the DID's destination;
calls between extensions are not affected, and a call is checked once
(transfers later in the call are not). Kept where RingQ keeps it:
`v_domain_settings` (time zone, language), `q_time_schedules` (office and
break hours, their actions, the holiday sound), `q_holidays`,
`q_holiday_exemptions`.

---

## 15. Outgoing calls: outbound rules

**Settings → Outbound rules → New outbound rule** (or Settings → Global
→ Outbound Rules). The form has RingQ's three parts:

**Matching calls**

| Field | Example |
|---|---|
| Rule name | `Singapore mobiles` (at least 4 characters) |
| Calls from extensions | **All extensions**, or **Selected extensions** (tick them) |
| Calls from extension range | type `100-199` and press Space; add as many as you need |
| Calls to numbers starting with prefix | `9`, or `+65` |
| Calls to numbers with a length of | `8`; also `8,10` or `7-15`. Digits, the prefix included |

Leave prefix and length empty and use **Use a pattern instead** for a
regular expression (rules made before 0.5.2 keep their pattern).

**Route settings**

| Field | Example |
|---|---|
| Route | your gateway, or **BLOCK** (the call is refused) |
| Strip digits | `1` if staff dial 0 or 9 first and the carrier must not get it |
| Prepend | `+65` if the carrier needs the country code |
| Outbound caller ID | one of your numbers (DIDs), or each extension's own |
| Backup route | a second gateway, tried when the first fails |
| Order | lower runs first; the first matching rule is used |

**Outbound call settings** (how the call presents itself to the carrier):

| Field | Choices |
|---|---|
| SIP from user | **Caller ID from Outbound rule** (the caller ID above), Gateway account, Extension number |
| SIP from display name | **Caller ID from Outbound rule**, Caller ID number, Extension name, Default |
| SIP from host | **Domain** (your company domain), or Gateway |
| CID type | **PAI** (P-Asserted-Identity), RPID (Remote-Party-ID), None |

New rules start with RingQ's choices (bold). If a carrier rejects calls
after you move to a new rule, try **SIP from user: Gateway account** and
**SIP from host: Gateway**. Internal numbers always win over outbound
rules.

## 15.1 Global Settings, SIP Profile and Network

**Settings → Global** shows tiles like RingQ. Three of them are new pages:

- **SIP Profile**: the SIP ports. **internal** is the main port from
  `ringqswitch.xml` (5060). Profiles from the RingQ database, or ones you
  **Add** here, open their own port (e.g. **external** on 5080). Calls and
  registrations arriving there are answered from it, and gateways whose
  profile is `external` send from it. **Status** shows whether the port is
  open (or why not, e.g. "address already in use"). RingQswitch always
  gives out this server's own address; an IP copied from the old RingQ
  server is shown with a warning and not used. SIP over TLS is not
  supported yet. Open every port in the firewall.
- **Network**: this server's SIP, audio and web addresses and ports, and
  the `ufw` commands that open them.
- **Service Restart** opens the System page (reload the configuration,
  repair defaults).

---

## 16. Feature codes (dial from any phone or the dialer)

| Dial | What it does |
|---|---|
| `*8` + extension | Answer a call ringing on that extension (BLF keys use it while the lamp blinks) |
| `*8` | Answer a call ringing in your pickup group |
| `**` + extension | The same as `*8` + extension |
| `*591` … `*595` | Parking slots 1 to 5: during a call, transfer to the slot to park the call (the caller hears music; the slot's lamp lights); when idle, dial it to take the parked call. Shared with the apps and the dialer's Parked tab. A call nobody takes goes back to whoever parked it after 2 minutes |
| `*5901` … `*5999` | More parking slots, the same way |
| `*26` + number | Your agent status: `*261` Available, `*262` the first aux code... (the numbers are on the Aux codes page). A short rising tone when it worked, an error tone when not |
| `*22` | Agent log in / log out |
| `*23` | Agent break on / off |
| `*78` / `*79` / `*77` | Do not disturb on / off / on-off: the same setting as in the apps and the dialer (callers hear busy, or go to voicemail) |
| Your own extension | Your voicemail (the phone's Message key): "You have N new messages", then each message; after one, `1` hear it again, `2` save, `7` delete, `5` call back, `#` next |
| `*97` | Your voicemail (the same) |
| `*99` + extension | Leave a voicemail for that extension (transfer a caller to it, e.g. `*991003`) |
| `*88` + team | Leave a message in a team (group) voicemail box |
| `*69` | Call back the last caller |
| `*870` | Redial the last number |
| `*80` + extension | Intercom: the other phone answers by itself on speaker |
| `9196` / `9197` | Echo test / test tone |

The server adds the codes your database does not have yet when it starts
(a RingQ database that already has a code keeps its own).

## 16A. Desk phone keys (BLF)

The programmable keys of a desk phone come from the extension's **Phone
keys** (Extensions → the extension → Phone keys), one row per key in order:
row 1 is key 2, row 2 key 3... (key 1 stays the phone's own line). Save the
rows, then **Reboot phone** so it fetches them (the phone gets its keys from
RingQ's provisioning, which reads the same rows; Reboot sends the phone a
check-sync).

| Type | What the key does | Value |
|---|---|---|
| Extension (BLF) | The lamp shows if that extension is idle, ringing or on a call. Press while it rings to pick the call up; when idle to call it | The extension number, e.g. 1045 |
| Speed Dial | Calls a number with one press | Any number or extension |
| Shared Parking | Park key for one slot (lit while a call is parked): press during a call to park it, while lit to take the parked call | Parking Slot 1 to 5 (`*591`-`*595`) |
| Agent Status | Sets your agent status in all your queues | An aux code, or Available (it dials `*26` + its number) |
| Change Status | IN sets Available (`*261`), OUT the first aux code (`*262`) | IN or OUT |
| Blank | An empty key | |

**Settings → Phone keys** holds the company's keys (reception, parking
slots...), shown in every dialer after the extension's own keys ("Show in
every dialer").

What the phones get from RingQswitch:

- **Lamps**: phones subscribe (SIP SUBSCRIBE, `Event: dialog`) to the
  extensions and parking slots of their keys; RingQswitch tells them at once
  when a call rings, is answered, ends, or is parked. A ringing key picks
  the call up with `*8` + extension, or by the call's dialog (phones that
  pick up that way).
- **Message key**: the phone's voicemail number is its own extension; its
  message lamp follows the mailbox (`Event: message-summary`).
- **DND key**: `*78` / `*79`, and on phones that support it the DND key
  and forwarding stay in step with the apps (`Event: as-feature-event`).

**The dialer's BLF and Parked tabs**: click the small **<** handle on the
dialer's left edge (halfway down). Two tabs show against the dialer:

- **BLF** (a red dot blinks on it while one of the extensions rings): the
  same keys with their lamps (green idle, blinking red ringing with who is
  calling, red on a call with for how long; amber: your current status),
  your own keys first, then the company's.
- **Parked** (the number shows how many calls are parked): slots P1 to P5,
  *Empty* or who is parked there and for how long (amber). The slots are
  the ones desk phones use (`*591` to `*595`).

Click a tab to slide its list out to the left of the dialer, click it again
(or the list's **×**) to slide it back, and click the handle (**>**) to fold
the tabs away. When the dialer was dragged to the left edge of the screen
they open on its right instead; on a phone they open over the dialer. The
live transcript panel moves out of their way.

Click a key as you would press it on a desk phone: a ringing extension is
picked up, an idle one is called, a speed dial calls its number, a status
key sets your agent status. During a call an extension key transfers the
call to it and a parking slot parks it there (the list says so at the top);
a lit slot gives the parked call back. In a call, **DTMF** opens the keypad
that sends tones (menus, PINs); type a sequence such as `1,,2345#` and
**Send** (a comma waits a second).

Quick test on a phone: save the keys and reboot the phone; press Message
(you hear your voicemail); press DND (the phone and the dialer show DND;
press again to turn it off); during a call press Park 1 (the caller hears
music, the key lights; press it again to take the call back); call an
extension that has a BLF key and press the key while it rings to pick up;
press an Agent Status key (a short tone, and the wallboard shows the new
status).

---

## 17. Reports

All reports have a **Period** (All, the default: the whole history; Today,
Yesterday, Last 7 days, Last 30 days or your own dates), show 50 rows per
page (Previous, page numbers, Next under the table) and have **Export CSV**
(every row of the period, not only the page shown). A **User** sees only
their own calls and agent records; an Owner sees the whole domain.

| Report | Shows |
|---|---|
| Call history | Every call: time, direction (icon: in, out, internal), from, to, result with who answered and who hung up, waiting and talk time, queue and agent. The recording's **play** icon (a square while playing: click to stop) and **download** icon. **Transcript** and **Sentiment** (section 12D). **Click a call** to open its path under it: who called and through which trunk, where it was routed, the IVR menus and keys pressed, the queue or ring group, who rang, who answered, who hung up and how the call ended, each with its time. Calls from before 0.6.1 (or written by the previous switch) show a shorter path rebuilt from the call record. **Result**: *answered* (someone answered: an extension, an agent, the number dialled), *missed* (nobody answered, also a caller who hung up in a queue before an agent answered, although the queue picked up to play its music), *voicemail* (a message was left), *busy*, *cancelled* (an outgoing call given up), *failed*. |
| Agent status times | Time each agent spent logged in, available and on each break |
| Agent status log | Every status / AUX change |
| Queue report | Per queue: offered, answered, abandoned, timed out, answer rate, service level, average and longest wait, average talk |
| Agent performance | Queue calls answered per agent, talk time |
| Queue calls | Every queue call and how it ended, with its **Transcript** and **Sentiment** (section 12D) |

**Voicemail** (Communicate menu, and the dialer's Voicemail tab): the
messages with who left them, when, how long, New or Heard, **what they
said** (with voicemail transcription on: click the text to read it all
and play it from any line) and its sentiment; play, download and delete
are icons. Users see only their own box.

---

## 18. Sounds and music on hold

**Settings → Sounds & music**

- **Prompts**: greetings for IVR menus, queues and ring groups. Upload a WAV
  or MP3 file and name it, e.g. `welcome`. Then choose `prompts/welcome.wav` in the
  IVR or queue form.
- **Music on hold**: WAV files in the folder **default** replace the
  built-in melody everywhere. Another folder (e.g. `sales`) can be chosen
  for one queue.

WAV and MP3 files work: uploads are converted to the telephone format
(8 kHz mono) by RingQswitch.

---

## 19. When something does not work

| Problem | What to do |
|---|---|
| `make build`: "go.mod requires go >= 1.24.0" | Install Go 1.24 (section 1, Step 1, Option B), or use the prebuilt package (Option A) |
| The page does not open | Use **https**://…:8443. Check `sudo systemctl status ringqswitch` and `sudo ufw status` (8443/tcp). With your own certificate, use the name (wasip.ringq.io), not the IP. |
| "wrong user name or password" as 1000 | The extension needs web access with a **web password** (section 1, Step 3). The SIP password does not work for the web. |
| "several domains have extension 1000" | Sign in as `1000@your.domain`. |
| A menu item is missing | Your role does not include it (section 3). Sign in as an Owner or admin. |
| "too many failed logins" | Wait 10 minutes. |
| The desk phone and the dialer push each other offline | The extension allows only one device. On the extension form, set **Advanced → Devices allowed at once** to 2 or more. |
| The dialer stays on "Not registered" | See **Settings → Event log** and `sudo journalctl -u ringqswitch -n 50 \| grep -i dialer`. |
| Dialer: **the other side is quiet** or **you are quiet** | Dialer → Settings: check that **Microphone** and **Speaker** are your headset, raise **Microphone boost** (they hear you louder) or **Speaker volume**, and watch **Your voice level** move when you speak. Also check the volume settings of your computer. |
| Dialer: **one-way audio** (you do not hear them, or they do not hear you) | During the call press **Audio check**: the red line shows where the sound stops. "Other side → server: Nothing arrives" means the phone's or carrier's audio does not reach the server: open UDP 16384-32768 in the server firewall **and** your cloud provider's firewall (section 0, step 4). "Your mic → server: Only silence" means your microphone is muted or the wrong one. "Server → your speaker: Not playing" means the browser held the sound back: click **Turn on sound**. The log also says it: `sudo journalctl -u ringqswitch \| grep -E "one-way\|no audio received"`. |
| Dialer: **delay** before you hear the other side | Dialer → Settings → **Audio path**. "TCP" means UDP 8443 is blocked (the server firewall, your cloud provider's firewall, or the office network): open it, then stop and start the dialer. On "UDP", the delay comes from the network or the carrier. |
| Meeting: **Transcribe** writes nothing for one person | Their browser has no built-in speech recognition, or their microphone is muted. Ask them to use a browser that has it. |
| Meeting: a presented **PDF** shows nothing | Some browsers (and phones) cannot show PDFs inside a page. Present a picture (PNG or JPEG) of the slide instead, or share your screen. |
| Dialer or meeting: connected but **no sound or video** | Upgrade to 0.4.2 first: 0.4.1 and older had no audio on servers with `ext-rtp-ip` or `public-ip` set. Then look at the dialer's red box: it shows the address it tried. Run `sudo ufw allow 8443/udp`, and also open it in your cloud provider's firewall if you use one. `sudo journalctl -u ringqswitch \| grep "dialer: audio"` shows "connected via udp/tcp" or "could not connect" with the addresses offered. |
| The dialer's call timer differs from the dashboard | Fixed in 0.4.2 (the dialer used the PC's clock). |
| The browser does not ask for the microphone | The page must be https. Click the padlock → Site settings → Microphone → Allow. |
| Dashboard shows more phones than you have | The browser dialer counts as a phone ("Web Dialer"). A desk phone that re-registers from a new port now replaces its old entry instead of adding one. |
| Agents available is 0 | Agents start **Logged out**. Make them Available (section 12.4). Only extensions in a queue, or with "Takes queue calls" on, are agents. |
| Listen / Barge in does nothing | It rings **your** extension. Answer that phone or the browser dialer. The admin account is asked which extension to ring. |
| Locked yourself out with a web allow-list rule | On the server: `sudo -u postgres psql ringqswitch -c "delete from rq_security_rules where rule_type='web-allow'"`. It applies within seconds. |
| Certificate expired or not renewed | `sudo certbot renew`, then check `ls -l /etc/ringqswitch/tls/wasip.ringq.io/`. Running `sudo bash packaging/use-certificate.sh wasip.ringq.io` again repairs it. |
| Database tool: **permission denied for table rq_…** | Fixed in 0.5.2: restart RingQswitch once and it shares its tables with the database owner and the owner of the RingQ tables. If your database tool signs in as yet another user, add it to `<database>`: `<param name="grant-to" value="ringq"/>` and restart. At once, by hand: `sudo -u postgres psql YOURDB -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ringq"`. |
| Carrier calls to port **5080** get no answer | Settings → Global → **SIP Profile**: the external profile must be enabled and "Listening". Then `sudo ufw allow 5080/udp`. |
| Something else | **Settings → System → Repair defaults**, and the full log: `sudo journalctl -u ringqswitch -n 200`. |

---

## 20. Where the settings are stored (for your developers)

RingQ tables, shared with the RingQ portal:

- Gateways: `v_gateways`
- DIDs: `v_destinations`
- Extensions: `v_extensions`
- Ring groups: `v_ring_groups`
- IVR menus: `v_ivr_menus`
- Queues, agents and tiers: `v_call_center_*`
- Voicemail: `v_voicemails` and `v_voicemail_messages`
- Conferences: `v_conferences`
- Carrier ACL: `v_access_control_nodes` (the "providers" list)
- Call routing: generated into `v_dialplans`

RingQswitch's own tables start with `rq_`:

- Call centre: `rq_aux_codes`, `rq_agent_state`, `rq_agent_status_log`,
  `rq_queue_calls`
- Routing: `rq_outbound_routes`, `rq_time_conditions`
- Web logins: `rq_web_users`, which stores the role and a hashed web
  password, never the plain password
- Chat: `rq_chat_rooms`, `rq_chat_members`, `rq_chat_messages`
- Meetings: `rq_meetings`
- Security rules: `rq_security_rules`

The JSON and WebSocket API is described in docs/API.md (section 10).
