'use strict';
// Languages of the web interface (the top bar's globe). The menu, the top
// bar, the user menu, page titles, table headings, form labels, common
// buttons, Call status, notifications and the sign-in page are translated;
// longer help texts stay in English. Arabic is shown right to left.

const I18N = (() => {
  // [code, English name, own name]
  const LANGS = [
    ['en', 'English', 'English'], ['ar', 'Arabic', 'العربية'], ['fr', 'French', 'Français'], ['es', 'Spanish', 'Español'],
    ['pt-BR', 'Portuguese (Brazil)', 'Português (Brasil)'], ['de', 'German', 'Deutsch'], ['zh-CN', 'Chinese (Simplified)', '简体中文'],
    ['ja', 'Japanese', '日本語'], ['ko', 'Korean', '한국어'], ['vi', 'Vietnamese', 'Tiếng Việt'], ['ms', 'Malay', 'Bahasa Melayu'],
    ['ta', 'Tamil', 'தமிழ்'], ['hi', 'Hindi', 'हिन्दी'],
  ];
  // The order of the translations in D.
  const ORDER = ['ar', 'fr', 'es', 'pt-BR', 'de', 'zh-CN', 'ja', 'ko', 'vi', 'ms', 'ta', 'hi'];
  const D = {
    // Menu
    "Dashboard": ["لوحة المعلومات", "Tableau de bord", "Panel", "Painel", "Dashboard", "仪表板", "ダッシュボード", "대시보드", "Bảng điều khiển", "Papan pemuka", "முகப்பு", "डैशबोर्ड"],
    "Team directory": ["دليل الفريق", "Annuaire de l'équipe", "Directorio del equipo", "Diretório da equipe", "Teamverzeichnis", "团队通讯录", "チームディレクトリ", "팀 디렉터리", "Danh bạ nhóm", "Direktori pasukan", "குழு அடைவு", "टीम निर्देशिका"],
    "Accounts": ["الحسابات", "Comptes", "Cuentas", "Contas", "Konten", "账户", "アカウント", "계정", "Tài khoản", "Akaun", "கணக்குகள்", "खाते"],
    "Extensions": ["الأرقام الداخلية", "Postes", "Extensiones", "Ramais", "Nebenstellen", "分机", "内線", "내선", "Máy nhánh", "Sambungan", "நீட்டிப்புகள்", "एक्सटेंशन"],
    "User roles": ["أدوار المستخدمين", "Rôles des utilisateurs", "Roles de usuario", "Funções de usuário", "Benutzerrollen", "用户角色", "ユーザーロール", "사용자 역할", "Vai trò người dùng", "Peranan pengguna", "பயனர் பாத்திரங்கள்", "उपयोगकर्ता भूमिकाएँ"],
    "Ring groups": ["مجموعات الرنين", "Groupes d'appel", "Grupos de timbre", "Grupos de toque", "Rufgruppen", "振铃组", "着信グループ", "링 그룹", "Nhóm đổ chuông", "Kumpulan deringan", "அழைப்புக் குழுக்கள்", "रिंग समूह"],
    "IVR menus": ["قوائم IVR", "Menus SVI", "Menús IVR", "Menus URA", "IVR-Menüs", "IVR 菜单", "IVR メニュー", "IVR 메뉴", "Menu IVR", "Menu IVR", "IVR பட்டியல்கள்", "IVR मेनू"],
    "Office hours": ["ساعات العمل", "Heures d'ouverture", "Horario de oficina", "Horário comercial", "Geschäftszeiten", "办公时间", "営業時間", "업무 시간", "Giờ làm việc", "Waktu pejabat", "அலுவலக நேரம்", "कार्यालय समय"],
    "Conference rooms": ["غرف المؤتمرات", "Salles de conférence", "Salas de conferencia", "Salas de conferência", "Konferenzräume", "会议室", "会議室", "회의실", "Phòng hội nghị", "Bilik persidangan", "மாநாட்டு அறைகள்", "कॉन्फ़्रेंस रूम"],
    "Contact center": ["مركز الاتصال", "Centre de contact", "Centro de contacto", "Central de atendimento", "Contact Center", "联络中心", "コンタクトセンター", "컨택 센터", "Trung tâm liên lạc", "Pusat hubungan", "தொடர்பு மையம்", "संपर्क केंद्र"],
    "Contacts": ["جهات الاتصال", "Contacts", "Contactos", "Contatos", "Kontakte", "联系人", "連絡先", "연락처", "Danh bạ", "Kenalan", "தொடர்புகள்", "संपर्क"],
    "Call status": ["حالة المكالمات", "État des appels", "Estado de llamadas", "Status das chamadas", "Anrufstatus", "通话状态", "通話ステータス", "통화 상태", "Trạng thái cuộc gọi", "Status panggilan", "அழைப்பு நிலை", "कॉल स्थिति"],
    "Call centre (live)": ["مركز الاتصال (مباشر)", "Centre d'appels (direct)", "Centro de llamadas (en vivo)", "Call center (ao vivo)", "Callcenter (live)", "呼叫中心（实时）", "コールセンター（ライブ）", "콜센터(실시간)", "Tổng đài (trực tiếp)", "Pusat panggilan (langsung)", "அழைப்பு மையம் (நேரலை)", "कॉल सेंटर (लाइव)"],
    "Queues": ["قوائم الانتظار", "Files d'attente", "Colas", "Filas", "Warteschlangen", "队列", "キュー", "대기열", "Hàng đợi", "Baris gilir", "வரிசைகள்", "कतारें"],
    "AUX / break codes": ["رموز الاستراحة", "Codes de pause", "Códigos de pausa", "Códigos de pausa", "Pausencodes", "小休代码", "離席コード", "휴식 코드", "Mã tạm nghỉ", "Kod rehat", "இடைவேளை குறியீடுகள்", "ब्रेक कोड"],
    "Wallboard": ["لوحة العرض", "Tableau mural", "Panel de pared", "Painel de parede", "Wallboard", "监控大屏", "ウォールボード", "월보드", "Bảng theo dõi", "Papan dinding", "சுவர் பலகை", "वॉलबोर्ड"],
    "Communicate": ["التواصل", "Communiquer", "Comunicar", "Comunicar", "Kommunikation", "沟通", "コミュニケーション", "커뮤니케이션", "Liên lạc", "Komunikasi", "தொடர்பாடல்", "संवाद"],
    "Chat": ["الدردشة", "Discussion", "Chat", "Chat", "Chat", "聊天", "チャット", "채팅", "Trò chuyện", "Sembang", "அரட்டை", "चैट"],
    "Meetings": ["الاجتماعات", "Réunions", "Reuniones", "Reuniões", "Besprechungen", "会议", "会議", "회의", "Cuộc họp", "Mesyuarat", "கூட்டங்கள்", "मीटिंग"],
    "Voicemail": ["البريد الصوتي", "Messagerie vocale", "Buzón de voz", "Correio de voz", "Voicemail", "语音信箱", "ボイスメール", "음성 메일", "Thư thoại", "Mel suara", "குரலஞ்சல்", "वॉइसमेल"],
    "Connections": ["الاتصالات", "Connexions", "Conexiones", "Conexões", "Verbindungen", "连接", "接続", "연결", "Kết nối", "Talian luar", "இணைப்புகள்", "कनेक्शन"],
    "Gateways (SIP trunks)": ["البوابات (خطوط SIP)", "Passerelles (trunks SIP)", "Pasarelas (troncales SIP)", "Gateways (troncos SIP)", "Gateways (SIP-Trunks)", "网关（SIP 中继）", "ゲートウェイ（SIP トランク）", "게이트웨이(SIP 트렁크)", "Cổng (trung kế SIP)", "Get laluan (trunk SIP)", "நுழைவாயில்கள் (SIP டிரங்குகள்)", "गेटवे (SIP ट्रंक)"],
    "Inbound numbers (DIDs)": ["الأرقام الواردة (DID)", "Numéros entrants (SDA)", "Números entrantes (DID)", "Números de entrada (DID)", "Eingehende Rufnummern (DID)", "呼入号码（DID）", "着信番号（DID）", "수신 번호(DID)", "Số gọi vào (DID)", "Nombor masuk (DID)", "உள்வரும் எண்கள் (DID)", "इनबाउंड नंबर (DID)"],
    "Outbound rules": ["قواعد الاتصال الصادر", "Règles sortantes", "Reglas salientes", "Regras de saída", "Ausgehende Regeln", "呼出规则", "発信ルール", "발신 규칙", "Quy tắc gọi ra", "Peraturan keluar", "வெளிச்செல்லும் விதிகள்", "आउटबाउंड नियम"],
    "Reports": ["التقارير", "Rapports", "Informes", "Relatórios", "Berichte", "报表", "レポート", "보고서", "Báo cáo", "Laporan", "அறிக்கைகள்", "रिपोर्ट"],
    "Call history": ["سجل المكالمات", "Historique des appels", "Historial de llamadas", "Histórico de chamadas", "Anrufverlauf", "通话记录", "通話履歴", "통화 기록", "Lịch sử cuộc gọi", "Sejarah panggilan", "அழைப்பு வரலாறு", "कॉल इतिहास"],
    "Agent status times": ["أوقات حالة الوكلاء", "Durées des statuts agents", "Tiempos de estado de agentes", "Tempos de status dos agentes", "Agenten-Statuszeiten", "坐席状态时长", "エージェント状態時間", "상담원 상태 시간", "Thời gian trạng thái nhân viên", "Masa status ejen", "முகவர் நிலை நேரங்கள்", "एजेंट स्थिति समय"],
    "Agent status log": ["سجل حالة الوكلاء", "Journal des statuts agents", "Registro de estado de agentes", "Registro de status dos agentes", "Agenten-Statusprotokoll", "坐席状态日志", "エージェント状態ログ", "상담원 상태 로그", "Nhật ký trạng thái nhân viên", "Log status ejen", "முகவர் நிலைப் பதிவு", "एजेंट स्थिति लॉग"],
    "Queue report": ["تقرير قوائم الانتظار", "Rapport des files", "Informe de colas", "Relatório de filas", "Warteschlangenbericht", "队列报表", "キューレポート", "대기열 보고서", "Báo cáo hàng đợi", "Laporan baris gilir", "வரிசை அறிக்கை", "कतार रिपोर्ट"],
    "Agent performance": ["أداء الوكلاء", "Performance des agents", "Rendimiento de agentes", "Desempenho dos agentes", "Agentenleistung", "坐席绩效", "エージェント実績", "상담원 성과", "Hiệu suất nhân viên", "Prestasi ejen", "முகவர் செயல்திறன்", "एजेंट प्रदर्शन"],
    "Queue calls": ["مكالمات قوائم الانتظار", "Appels des files", "Llamadas en cola", "Chamadas da fila", "Warteschlangenanrufe", "队列通话", "キュー通話", "대기열 통화", "Cuộc gọi hàng đợi", "Panggilan baris gilir", "வரிசை அழைப்புகள்", "कतार कॉल"],
    "Settings": ["الإعدادات", "Paramètres", "Configuración", "Configurações", "Einstellungen", "设置", "設定", "설정", "Cài đặt", "Tetapan", "அமைப்புகள்", "सेटिंग्स"],
    "Global": ["عام", "Global", "Global", "Global", "Global", "全局", "グローバル", "전역", "Toàn cục", "Global", "பொது", "ग्लोबल"],
    "Sounds & music": ["الأصوات والموسيقى", "Sons et musique", "Sonidos y música", "Sons e música", "Klänge & Musik", "声音和音乐", "音声と音楽", "소리 및 음악", "Âm thanh & nhạc", "Bunyi & muzik", "ஒலிகள் & இசை", "ध्वनि और संगीत"],
    "Security": ["الأمان", "Sécurité", "Seguridad", "Segurança", "Sicherheit", "安全", "セキュリティ", "보안", "Bảo mật", "Keselamatan", "பாதுகாப்பு", "सुरक्षा"],
    "Domains": ["النطاقات", "Domaines", "Dominios", "Domínios", "Domänen", "域", "ドメイン", "도메인", "Tên miền", "Domain", "டொமைன்கள்", "डोमेन"],
    "Dialplans (advanced)": ["خطط الاتصال (متقدم)", "Plans de numérotation (avancé)", "Planes de marcado (avanzado)", "Planos de discagem (avançado)", "Wählpläne (erweitert)", "拨号计划（高级）", "ダイヤルプラン（詳細）", "다이얼 플랜(고급)", "Kế hoạch quay số (nâng cao)", "Pelan dail (lanjutan)", "டயல் திட்டங்கள் (மேம்பட்ட)", "डायलप्लान (उन्नत)"],
    "System": ["النظام", "Système", "Sistema", "Sistema", "System", "系统", "システム", "시스템", "Hệ thống", "Sistem", "அமைப்பு", "सिस्टम"],
    "Event log": ["سجل الأحداث", "Journal des événements", "Registro de eventos", "Registro de eventos", "Ereignisprotokoll", "事件日志", "イベントログ", "이벤트 로그", "Nhật ký sự kiện", "Log peristiwa", "நிகழ்வுப் பதிவு", "इवेंट लॉग"],
    "My account": ["حسابي", "Mon compte", "Mi cuenta", "Minha conta", "Mein Konto", "我的账户", "マイアカウント", "내 계정", "Tài khoản của tôi", "Akaun saya", "என் கணக்கு", "मेरा खाता"],
    // Top bar
    "Search": ["بحث", "Rechercher", "Buscar", "Pesquisar", "Suchen", "搜索", "検索", "검색", "Tìm kiếm", "Cari", "தேடு", "खोजें"],
    "Meetings calendar": ["تقويم الاجتماعات", "Calendrier des réunions", "Calendario de reuniones", "Calendário de reuniões", "Besprechungskalender", "会议日历", "会議カレンダー", "회의 일정", "Lịch họp", "Kalendar mesyuarat", "கூட்ட நாட்காட்டி", "मीटिंग कैलेंडर"],
    "Meet": ["اجتماع", "Réunion", "Reunión", "Reunião", "Meeting", "会议", "ミーティング", "미팅", "Họp", "Mesyuarat", "சந்திப்பு", "मीट"],
    "Clear the RingQswitch cache": ["مسح ذاكرة RingQswitch المؤقتة", "Vider le cache de RingQswitch", "Borrar la caché de RingQswitch", "Limpar o cache do RingQswitch", "RingQswitch-Cache leeren", "清除 RingQswitch 缓存", "RingQswitch のキャッシュをクリア", "RingQswitch 캐시 지우기", "Xóa bộ nhớ đệm RingQswitch", "Kosongkan cache RingQswitch", "RingQswitch தற்காலிக நினைவகத்தை அழி", "RingQswitch कैश साफ़ करें"],
    "Language": ["اللغة", "Langue", "Idioma", "Idioma", "Sprache", "语言", "言語", "언어", "Ngôn ngữ", "Bahasa", "மொழி", "भाषा"],
    "Dark mode": ["الوضع الداكن", "Mode sombre", "Modo oscuro", "Modo escuro", "Dunkelmodus", "深色模式", "ダークモード", "다크 모드", "Chế độ tối", "Mod gelap", "இருண்ட பயன்முறை", "डार्क मोड"],
    "Light mode": ["الوضع الفاتح", "Mode clair", "Modo claro", "Modo claro", "Hellmodus", "浅色模式", "ライトモード", "라이트 모드", "Chế độ sáng", "Mod cerah", "ஒளி பயன்முறை", "लाइट मोड"],
    "Collapse the menu": ["طي القائمة", "Réduire le menu", "Contraer el menú", "Recolher o menu", "Menü einklappen", "收起菜单", "メニューを折りたたむ", "메뉴 접기", "Thu gọn menu", "Kecilkan menu", "பட்டியலைச் சுருக்கு", "मेनू छोटा करें"],
    "Expand the menu": ["توسيع القائمة", "Développer le menu", "Expandir el menú", "Expandir o menu", "Menü ausklappen", "展开菜单", "メニューを展開", "메뉴 펼치기", "Mở rộng menu", "Besarkan menu", "பட்டியலை விரி", "मेनू बड़ा करें"],
    // User menu
    "Online": ["متصل", "En ligne", "En línea", "Online", "Online", "在线", "オンライン", "온라인", "Trực tuyến", "Dalam talian", "இணைப்பில்", "ऑनलाइन"],
    "On break": ["في استراحة", "En pause", "En descanso", "Em pausa", "In Pause", "小休中", "休憩中", "휴식 중", "Đang nghỉ", "Sedang rehat", "இடைவேளையில்", "ब्रेक पर"],
    "Do not disturb": ["عدم الإزعاج", "Ne pas déranger", "No molestar", "Não perturbe", "Nicht stören", "请勿打扰", "応答不可", "방해 금지", "Không làm phiền", "Jangan ganggu", "தொந்தரவு செய்ய வேண்டாம்", "परेशान न करें"],
    "Ext": ["تحويلة", "Poste", "Ext.", "Ramal", "Nst.", "分机", "内線", "내선", "Máy nhánh", "Samb.", "நீட்டிப்பு", "एक्सटेंशन"],
    "Connected devices": ["الأجهزة المتصلة", "Appareils connectés", "Dispositivos conectados", "Dispositivos conectados", "Verbundene Geräte", "已连接的设备", "接続中のデバイス", "연결된 장치", "Thiết bị đã kết nối", "Peranti bersambung", "இணைக்கப்பட்ட சாதனங்கள்", "कनेक्टेड डिवाइस"],
    "Refresh": ["تحديث", "Actualiser", "Actualizar", "Atualizar", "Aktualisieren", "刷新", "更新", "새로 고침", "Làm mới", "Muat semula", "புதுப்பி", "रीफ़्रेश"],
    "Loading…": ["جارٍ التحميل…", "Chargement…", "Cargando…", "Carregando…", "Wird geladen…", "加载中…", "読み込み中…", "불러오는 중…", "Đang tải…", "Memuatkan…", "ஏற்றுகிறது…", "लोड हो रहा है…"],
    "Live connections unavailable right now.": ["الاتصالات المباشرة غير متاحة الآن.", "Connexions en direct indisponibles pour le moment.", "Conexiones en vivo no disponibles ahora.", "Conexões ao vivo indisponíveis no momento.", "Live-Verbindungen sind gerade nicht verfügbar.", "实时连接暂不可用。", "ライブ接続は現在利用できません。", "지금은 실시간 연결을 사용할 수 없습니다.", "Hiện không có kết nối trực tiếp.", "Sambungan langsung tidak tersedia sekarang.", "நேரலை இணைப்புகள் இப்போது கிடைக்கவில்லை.", "लाइव कनेक्शन अभी उपलब्ध नहीं हैं।"],
    "No phone or app is signed in right now.": ["لا يوجد هاتف أو تطبيق مسجّل الآن.", "Aucun téléphone ni application connecté pour le moment.", "Ningún teléfono o aplicación conectado ahora.", "Nenhum telefone ou aplicativo conectado agora.", "Gerade ist kein Telefon und keine App angemeldet.", "当前没有已登录的电话或应用。", "現在サインインしている電話やアプリはありません。", "지금 로그인된 전화나 앱이 없습니다.", "Hiện không có điện thoại hoặc ứng dụng nào đăng nhập.", "Tiada telefon atau aplikasi log masuk sekarang.", "இப்போது எந்தத் தொலைபேசியும் செயலியும் உள்நுழையவில்லை.", "अभी कोई फ़ोन या ऐप साइन इन नहीं है।"],
    "Browser dialer": ["هاتف المتصفح", "Téléphone du navigateur", "Marcador del navegador", "Discador do navegador", "Browser-Telefon", "浏览器拨号器", "ブラウザーダイヤラー", "브라우저 다이얼러", "Trình quay số trên trình duyệt", "Pendail pelayar", "உலாவி டயலர்", "ब्राउज़र डायलर"],
    "SIP phone": ["هاتف SIP", "Téléphone SIP", "Teléfono SIP", "Telefone SIP", "SIP-Telefon", "SIP 电话", "SIP 電話", "SIP 전화", "Điện thoại SIP", "Telefon SIP", "SIP தொலைபேசி", "SIP फ़ोन"],
    "Signed in at": ["وقت تسجيل الدخول", "Connecté à", "Conectado a las", "Conectado às", "Angemeldet um", "登录时间", "サインイン時刻", "로그인 시각", "Đăng nhập lúc", "Log masuk pada", "உள்நுழைந்த நேரம்", "साइन इन समय"],
    "View profile": ["عرض الملف الشخصي", "Voir le profil", "Ver perfil", "Ver perfil", "Profil anzeigen", "查看个人资料", "プロフィールを表示", "프로필 보기", "Xem hồ sơ", "Lihat profil", "சுயவிவரத்தைக் காண்", "प्रोफ़ाइल देखें"],
    "Logout": ["تسجيل الخروج", "Déconnexion", "Cerrar sesión", "Sair", "Abmelden", "退出登录", "ログアウト", "로그아웃", "Đăng xuất", "Log keluar", "வெளியேறு", "लॉग आउट"],
    "Previous builds": ["الإصدارات السابقة", "Versions précédentes", "Versiones anteriores", "Versões anteriores", "Frühere Versionen", "历史版本", "以前のビルド", "이전 빌드", "Các bản trước", "Binaan terdahulu", "முந்தைய பதிப்புகள்", "पिछले बिल्ड"],
    "This server runs": ["يعمل هذا الخادم بالإصدار", "Ce serveur exécute", "Este servidor ejecuta", "Este servidor executa", "Dieser Server läuft mit", "此服务器运行", "このサーバーのバージョン:", "이 서버 버전:", "Máy chủ này chạy", "Pelayan ini menjalankan", "இந்தச் சேவையகம் இயக்குவது", "यह सर्वर चला रहा है"],
    "Installed": ["مُثبّت", "Installée", "Instalada", "Instalada", "Installiert", "已安装", "インストール済み", "설치됨", "Đã cài đặt", "Dipasang", "நிறுவப்பட்டது", "इंस्टॉल है"],
    "Close": ["إغلاق", "Fermer", "Cerrar", "Fechar", "Schließen", "关闭", "閉じる", "닫기", "Đóng", "Tutup", "மூடு", "बंद करें"],
    "Administrator": ["المسؤول", "Administrateur", "Administrador", "Administrador", "Administrator", "管理员", "管理者", "관리자", "Quản trị viên", "Pentadbir", "நிர்வாகி", "व्यवस्थापक"],
    "Owner": ["المالك", "Propriétaire", "Propietario", "Proprietário", "Inhaber", "所有者", "オーナー", "소유자", "Chủ sở hữu", "Pemilik", "உரிமையாளர்", "मालिक"],
    "Manager": ["المدير", "Responsable", "Gerente", "Gerente", "Manager", "经理", "マネージャー", "매니저", "Quản lý", "Pengurus", "மேலாளர்", "प्रबंधक"],
    "User": ["المستخدم", "Utilisateur", "Usuario", "Usuário", "Benutzer", "用户", "ユーザー", "사용자", "Người dùng", "Pengguna", "பயனர்", "उपयोगकर्ता"],
    // Clear the cache
    "Cache cleared": ["تم مسح الذاكرة المؤقتة", "Cache vidé", "Caché borrada", "Cache limpo", "Cache geleert", "缓存已清除", "キャッシュをクリアしました", "캐시를 지웠습니다", "Đã xóa bộ nhớ đệm", "Cache dikosongkan", "தற்காலிக நினைவகம் அழிக்கப்பட்டது", "कैश साफ़ हो गया"],
    "Reloaded from the database:": ["أُعيد التحميل من قاعدة البيانات:", "Rechargé depuis la base de données :", "Recargado desde la base de datos:", "Recarregado do banco de dados:", "Aus der Datenbank neu geladen:", "已从数据库重新加载：", "データベースから再読み込み:", "데이터베이스에서 다시 불러옴:", "Đã tải lại từ cơ sở dữ liệu:", "Dimuat semula dari pangkalan data:", "தரவுத்தளத்திலிருந்து மீண்டும் ஏற்றப்பட்டது:", "डेटाबेस से फिर लोड किया:"],
    "domains": ["نطاقات", "domaines", "dominios", "domínios", "Domänen", "个域", "ドメイン", "도메인", "tên miền", "domain", "டொமைன்கள்", "डोमेन"],
    "extensions": ["أرقام داخلية", "postes", "extensiones", "ramais", "Nebenstellen", "个分机", "内線", "내선", "máy nhánh", "sambungan", "நீட்டிப்புகள்", "एक्सटेंशन"],
    "dialplans": ["خطط اتصال", "plans de numérotation", "planes de marcado", "planos de discagem", "Wählpläne", "个拨号计划", "ダイヤルプラン", "다이얼 플랜", "kế hoạch quay số", "pelan dail", "டயல் திட்டங்கள்", "डायलप्लान"],
    "gateways": ["بوابات", "passerelles", "pasarelas", "gateways", "Gateways", "个网关", "ゲートウェイ", "게이트웨이", "cổng", "get laluan", "நுழைவாயில்கள்", "गेटवे"],
    // Sign in
    "Sign in with your extension and web password": ["سجّل الدخول برقمك الداخلي وكلمة مرور الويب", "Connectez-vous avec votre poste et votre mot de passe web", "Inicie sesión con su extensión y contraseña web", "Entre com seu ramal e senha web", "Melden Sie sich mit Ihrer Nebenstelle und Ihrem Web-Passwort an", "使用分机号和网页密码登录", "内線番号と Web パスワードでサインイン", "내선 번호와 웹 비밀번호로 로그인하세요", "Đăng nhập bằng máy nhánh và mật khẩu web", "Log masuk dengan sambungan dan kata laluan web anda", "உங்கள் நீட்டிப்பு மற்றும் வலை கடவுச்சொல்லுடன் உள்நுழையவும்", "अपने एक्सटेंशन और वेब पासवर्ड से साइन इन करें"],
    "Extension": ["الرقم الداخلي", "Poste", "Extensión", "Ramal", "Nebenstelle", "分机", "内線", "내선", "Máy nhánh", "Sambungan", "நீட்டிப்பு", "एक्सटेंशन"],
    "Web password": ["كلمة مرور الويب", "Mot de passe web", "Contraseña web", "Senha web", "Web-Passwort", "网页密码", "Web パスワード", "웹 비밀번호", "Mật khẩu web", "Kata laluan web", "வலை கடவுச்சொல்", "वेब पासवर्ड"],
    "Sign in": ["تسجيل الدخول", "Se connecter", "Iniciar sesión", "Entrar", "Anmelden", "登录", "サインイン", "로그인", "Đăng nhập", "Log masuk", "உள்நுழை", "साइन इन"],
    "The web password is set by your administrator under Extensions. It is not your phone's SIP password.": ["يحدد المسؤول كلمة مرور الويب في صفحة الأرقام الداخلية، وهي ليست كلمة مرور SIP لهاتفك.", "Le mot de passe web est défini par votre administrateur dans Postes. Ce n'est pas le mot de passe SIP de votre téléphone.", "Su administrador define la contraseña web en Extensiones. No es la contraseña SIP de su teléfono.", "A senha web é definida pelo administrador em Ramais. Não é a senha SIP do seu telefone.", "Das Web-Passwort legt Ihr Administrator unter Nebenstellen fest. Es ist nicht das SIP-Passwort Ihres Telefons.", "网页密码由管理员在“分机”中设置，不是电话的 SIP 密码。", "Web パスワードは管理者が「内線」で設定します。電話の SIP パスワードではありません。", "웹 비밀번호는 관리자가 내선 메뉴에서 설정합니다. 전화기의 SIP 비밀번호가 아닙니다.", "Mật khẩu web do quản trị viên đặt trong mục Máy nhánh. Đây không phải mật khẩu SIP của điện thoại.", "Kata laluan web ditetapkan oleh pentadbir di bawah Sambungan. Ia bukan kata laluan SIP telefon anda.", "வலை கடவுச்சொல்லை நிர்வாகி நீட்டிப்புகள் பக்கத்தில் அமைப்பார். இது உங்கள் தொலைபேசியின் SIP கடவுச்சொல் அல்ல.", "वेब पासवर्ड आपका व्यवस्थापक एक्सटेंशन में सेट करता है। यह आपके फ़ोन का SIP पासवर्ड नहीं है।"],
    // Call status
    "Search number, type, route": ["ابحث برقم أو نوع أو مسار", "Rechercher un numéro, type, route", "Buscar número, tipo, ruta", "Pesquisar número, tipo, rota", "Nummer, Typ, Route suchen", "搜索号码、类型、路由", "番号・種類・ルートで検索", "번호, 유형, 경로 검색", "Tìm số, loại, tuyến", "Cari nombor, jenis, laluan", "எண், வகை, வழியைத் தேடு", "नंबर, प्रकार, रूट खोजें"],
    "Active calls": ["المكالمات الجارية", "Appels en cours", "Llamadas activas", "Chamadas ativas", "Aktive Anrufe", "当前通话", "通話中", "진행 중인 통화", "Cuộc gọi đang diễn ra", "Panggilan aktif", "செயலில் உள்ள அழைப்புகள்", "सक्रिय कॉल"],
    "Online extensions": ["الأرقام الداخلية المتصلة", "Postes en ligne", "Extensiones en línea", "Ramais online", "Angemeldete Nebenstellen", "在线分机", "オンラインの内線", "온라인 내선", "Máy nhánh trực tuyến", "Sambungan dalam talian", "இணைப்பிலுள்ள நீட்டிப்புகள்", "ऑनलाइन एक्सटेंशन"],
    "Queue": ["قائمة الانتظار", "File", "Cola", "Fila", "Warteschlange", "队列", "キュー", "대기열", "Hàng đợi", "Baris gilir", "வரிசை", "कतार"],
    "Inbound": ["وارد", "Entrant", "Entrante", "Entrada", "Eingehend", "呼入", "着信", "수신", "Gọi vào", "Masuk", "உள்வரும்", "इनबाउंड"],
    "Outbound": ["صادر", "Sortant", "Saliente", "Saída", "Ausgehend", "呼出", "発信", "발신", "Gọi ra", "Keluar", "வெளிச்செல்லும்", "आउटबाउंड"],
    "Fax": ["فاكس", "Fax", "Fax", "Fax", "Fax", "传真", "FAX", "팩스", "Fax", "Faks", "தொலைநகல்", "फ़ैक्स"],
    "Internal": ["داخلي", "Interne", "Interna", "Interna", "Intern", "内部", "内線通話", "내부", "Nội bộ", "Dalaman", "உள்", "आंतरिक"],
    "Time": ["الوقت", "Heure", "Hora", "Hora", "Zeit", "时间", "時刻", "시간", "Thời gian", "Masa", "நேரம்", "समय"],
    "Direction": ["الاتجاه", "Sens", "Dirección", "Direção", "Richtung", "方向", "方向", "방향", "Chiều", "Arah", "திசை", "दिशा"],
    "From → To": ["من → إلى", "De → À", "De → A", "De → Para", "Von → An", "主叫 → 被叫", "発信元 → 宛先", "발신 → 수신", "Từ → Đến", "Dari → Ke", "இருந்து → வரை", "से → तक"],
    "Answered by": ["أجاب عليها", "Répondu par", "Atendida por", "Atendida por", "Angenommen von", "接听人", "応答者", "응답자", "Người trả lời", "Dijawab oleh", "பதிலளித்தவர்", "उत्तर देने वाला"],
    "Duration": ["المدة", "Durée", "Duración", "Duração", "Dauer", "时长", "通話時間", "통화 시간", "Thời lượng", "Tempoh", "கால அளவு", "अवधि"],
    "Status": ["الحالة", "Statut", "Estado", "Status", "Status", "状态", "ステータス", "상태", "Trạng thái", "Status", "நிலை", "स्थिति"],
    "Action": ["إجراء", "Action", "Acción", "Ação", "Aktion", "操作", "操作", "작업", "Thao tác", "Tindakan", "செயல்", "कार्रवाई"],
    "Rule / Route": ["القاعدة / المسار", "Règle / Route", "Regla / Ruta", "Regra / Rota", "Regel / Route", "规则 / 路由", "ルール / ルート", "규칙 / 경로", "Quy tắc / Tuyến", "Peraturan / Laluan", "விதி / வழி", "नियम / रूट"],
    "No calls in progress": ["لا توجد مكالمات جارية", "Aucun appel en cours", "No hay llamadas en curso", "Nenhuma chamada em andamento", "Keine laufenden Anrufe", "当前没有通话", "通話はありません", "진행 중인 통화가 없습니다", "Không có cuộc gọi nào", "Tiada panggilan sedang berlangsung", "நடப்பில் அழைப்புகள் இல்லை", "कोई कॉल जारी नहीं है"],
    "Previous calls": ["المكالمات السابقة", "Appels précédents", "Llamadas anteriores", "Chamadas anteriores", "Frühere Anrufe", "之前的通话", "過去の通話", "이전 통화", "Cuộc gọi trước", "Panggilan terdahulu", "முந்தைய அழைப்புகள்", "पिछली कॉल"],
    "Wait / Dur": ["الانتظار / المدة", "Attente / Durée", "Espera / Dur.", "Espera / Dur.", "Warten / Dauer", "等待 / 时长", "待ち / 通話", "대기 / 통화", "Chờ / Thời lượng", "Tunggu / Tempoh", "காத்திருப்பு / அளவு", "प्रतीक्षा / अवधि"],
    "No calls today yet": ["لا توجد مكالمات اليوم بعد", "Aucun appel aujourd'hui", "Aún no hay llamadas hoy", "Nenhuma chamada hoje ainda", "Heute noch keine Anrufe", "今天还没有通话", "本日の通話はまだありません", "오늘 통화가 아직 없습니다", "Hôm nay chưa có cuộc gọi", "Belum ada panggilan hari ini", "இன்று இன்னும் அழைப்புகள் இல்லை", "आज अभी तक कोई कॉल नहीं"],
    "Name": ["الاسم", "Nom", "Nombre", "Nome", "Name", "名称", "名前", "이름", "Tên", "Nama", "பெயர்", "नाम"],
    "Device": ["الجهاز", "Appareil", "Dispositivo", "Dispositivo", "Gerät", "设备", "デバイス", "장치", "Thiết bị", "Peranti", "சாதனம்", "डिवाइस"],
    "Address": ["العنوان", "Adresse", "Dirección", "Endereço", "Adresse", "地址", "アドレス", "주소", "Địa chỉ", "Alamat", "முகவரி", "पता"],
    "On a call": ["في مكالمة", "En communication", "En llamada", "Em chamada", "Im Gespräch", "通话中", "通話中", "통화 중", "Đang gọi", "Dalam panggilan", "அழைப்பில்", "कॉल पर"],
    "Idle": ["متاح", "Libre", "Libre", "Livre", "Frei", "空闲", "待機中", "대기", "Rảnh", "Lapang", "ஓய்வில்", "खाली"],
    "No extension is signed in": ["لا يوجد رقم داخلي مسجّل", "Aucun poste connecté", "Ninguna extensión conectada", "Nenhum ramal conectado", "Keine Nebenstelle angemeldet", "没有已登录的分机", "サインイン中の内線はありません", "로그인된 내선이 없습니다", "Không có máy nhánh nào đăng nhập", "Tiada sambungan log masuk", "எந்த நீட்டிப்பும் உள்நுழையவில்லை", "कोई एक्सटेंशन साइन इन नहीं है"],
    "Listen": ["استماع", "Écouter", "Escuchar", "Ouvir", "Mithören", "监听", "モニター", "듣기", "Nghe", "Dengar", "கேள்", "सुनें"],
    "Whisper": ["همس", "Chuchoter", "Susurrar", "Sussurrar", "Flüstern", "耳语", "ささやき", "귓속말", "Nói nhỏ", "Bisik", "கிசுகிசு", "व्हिस्पर"],
    "Barge in": ["مقاطعة", "Intervenir", "Intervenir", "Intervir", "Aufschalten", "强插", "割り込み", "끼어들기", "Tham gia", "Celah masuk", "இடைமறி", "बीच में जुड़ें"],
    "Hang up": ["إنهاء المكالمة", "Raccrocher", "Colgar", "Desligar", "Auflegen", "挂断", "切断", "끊기", "Gác máy", "Tamatkan", "துண்டி", "कॉल काटें"],
    "Hang up this call?": ["إنهاء هذه المكالمة؟", "Raccrocher cet appel ?", "¿Colgar esta llamada?", "Desligar esta chamada?", "Diesen Anruf beenden?", "挂断此通话？", "この通話を切断しますか？", "이 통화를 끊을까요?", "Gác cuộc gọi này?", "Tamatkan panggilan ini?", "இந்த அழைப்பைத் துண்டிக்கவா?", "यह कॉल काटें?"],
    "Call ended": ["انتهت المكالمة", "Appel terminé", "Llamada finalizada", "Chamada encerrada", "Anruf beendet", "通话已结束", "通話を終了しました", "통화 종료", "Cuộc gọi đã kết thúc", "Panggilan tamat", "அழைப்பு முடிந்தது", "कॉल समाप्त"],
    "Calling your extension…": ["جارٍ الاتصال برقمك الداخلي…", "Appel de votre poste…", "Llamando a su extensión…", "Chamando seu ramal…", "Ihre Nebenstelle wird angerufen…", "正在呼叫您的分机…", "あなたの内線を呼び出しています…", "내선으로 거는 중…", "Đang gọi máy nhánh của bạn…", "Memanggil sambungan anda…", "உங்கள் நீட்டிப்பை அழைக்கிறது…", "आपके एक्सटेंशन पर कॉल हो रही है…"],
    "Which extension should ring so you can listen?": ["أي رقم داخلي يجب أن يرن لتتمكن من الاستماع؟", "Quel poste doit sonner pour écouter ?", "¿Qué extensión debe sonar para escuchar?", "Qual ramal deve tocar para você ouvir?", "Welche Nebenstelle soll zum Mithören klingeln?", "要呼叫哪个分机来监听？", "監視用にどの内線を鳴らしますか？", "어느 내선으로 들을까요?", "Máy nhánh nào sẽ đổ chuông để bạn nghe?", "Sambungan mana patut berdering untuk anda dengar?", "நீங்கள் கேட்க எந்த நீட்டிப்பு ஒலிக்க வேண்டும்?", "सुनने के लिए कौन-सा एक्सटेंशन बजे?"],
    "Queue Answered": ["تم الرد من قائمة الانتظار", "Répondu (file)", "Atendida en cola", "Atendida na fila", "In Warteschlange angenommen", "队列已接听", "キュー応答", "대기열 응답", "Hàng đợi đã trả lời", "Dijawab dari baris gilir", "வரிசையில் பதிலளிக்கப்பட்டது", "कतार से उत्तर दिया"],
    "Queue Timeout": ["انتهت مهلة قائمة الانتظار", "Délai de file dépassé", "Tiempo de cola agotado", "Tempo da fila esgotado", "Warteschlangen-Zeitüberschreitung", "队列超时", "キュータイムアウト", "대기열 시간 초과", "Hết thời gian chờ", "Masa baris gilir tamat", "வரிசை நேரம் முடிந்தது", "कतार समय समाप्त"],
    "Queue Caller Abandoned": ["تخلّى المتصل في قائمة الانتظار", "Appelant de la file a raccroché", "Abandonada en cola", "Abandonada na fila", "Anrufer in Warteschlange aufgelegt", "队列来电者放弃", "キュー放棄", "대기열 발신자 포기", "Người gọi bỏ hàng đợi", "Pemanggil baris gilir meninggalkan", "வரிசையில் அழைப்பாளர் கைவிட்டார்", "कतार में कॉलर ने छोड़ा"],
    "Answered": ["تم الرد", "Répondu", "Atendida", "Atendida", "Angenommen", "已接听", "応答", "응답됨", "Đã trả lời", "Dijawab", "பதிலளிக்கப்பட்டது", "उत्तर दिया"],
    "Missed": ["فائتة", "Manqué", "Perdida", "Perdida", "Verpasst", "未接", "不在", "부재중", "Nhỡ", "Terlepas", "தவறியது", "छूटी"],
    "Cancelled": ["أُلغيت", "Annulé", "Cancelada", "Cancelada", "Abgebrochen", "已取消", "キャンセル", "취소됨", "Đã hủy", "Dibatalkan", "ரத்துசெய்யப்பட்டது", "रद्द"],
    "Failed": ["فشلت", "Échec", "Fallida", "Falhou", "Fehlgeschlagen", "失败", "失敗", "실패", "Thất bại", "Gagal", "தோல்வி", "विफल"],
    "Busy": ["مشغول", "Occupé", "Ocupado", "Ocupado", "Besetzt", "忙线", "話中", "통화 중", "Máy bận", "Sibuk", "பிஸி", "व्यस्त"],
    "Talking": ["يتحدث", "En conversation", "Hablando", "Falando", "Im Gespräch", "通话中", "通話中", "통화 중", "Đang nói chuyện", "Sedang bercakap", "பேசுகிறார்", "बात हो रही है"],
    "Ringing": ["يرن", "Sonnerie", "Sonando", "Tocando", "Klingelt", "振铃中", "呼出中", "벨 울림", "Đang đổ chuông", "Berdering", "ஒலிக்கிறது", "घंटी बज रही है"],
    "Waiting in queue": ["ينتظر في قائمة الانتظار", "En attente dans la file", "Esperando en cola", "Aguardando na fila", "Wartet in der Warteschlange", "队列等待中", "キュー待ち", "대기열 대기 중", "Đang chờ trong hàng đợi", "Menunggu dalam baris gilir", "வரிசையில் காத்திருக்கிறது", "कतार में प्रतीक्षा"],
    // Notifications
    "Incoming call": ["مكالمة واردة", "Appel entrant", "Llamada entrante", "Chamada recebida", "Eingehender Anruf", "来电", "着信", "수신 전화", "Cuộc gọi đến", "Panggilan masuk", "உள்வரும் அழைப்பு", "आने वाली कॉल"],
    "Missed call": ["مكالمة فائتة", "Appel manqué", "Llamada perdida", "Chamada perdida", "Verpasster Anruf", "未接来电", "不在着信", "부재중 전화", "Cuộc gọi nhỡ", "Panggilan terlepas", "தவறிய அழைப்பு", "छूटी हुई कॉल"],
    "New voicemail": ["بريد صوتي جديد", "Nouveau message vocal", "Nuevo mensaje de voz", "Novo correio de voz", "Neue Sprachnachricht", "新语音留言", "新しいボイスメール", "새 음성 메일", "Thư thoại mới", "Mel suara baharu", "புதிய குரலஞ்சல்", "नया वॉइसमेल"],
    "Answer": ["رد", "Répondre", "Contestar", "Atender", "Annehmen", "接听", "応答", "받기", "Trả lời", "Jawab", "பதிலளி", "उत्तर दें"],
    "Decline": ["رفض", "Refuser", "Rechazar", "Recusar", "Ablehnen", "拒绝", "拒否", "거절", "Từ chối", "Tolak", "நிராகரி", "अस्वीकार"],
    "Call back": ["معاودة الاتصال", "Rappeler", "Devolver llamada", "Retornar", "Zurückrufen", "回拨", "折り返す", "다시 걸기", "Gọi lại", "Panggil semula", "திரும்ப அழை", "वापस कॉल करें"],
    "Reply": ["إرسال رد", "Répondre", "Responder", "Responder", "Antworten", "回复", "返信", "답장", "Trả lời", "Balas", "பதில்", "जवाब दें"],
    "Admit": ["قبول", "Admettre", "Admitir", "Admitir", "Zulassen", "准入", "参加を許可", "입장 허용", "Cho vào", "Benarkan", "அனுமதி", "प्रवेश दें"],
    // Common
    "Save": ["حفظ", "Enregistrer", "Guardar", "Salvar", "Speichern", "保存", "保存", "저장", "Lưu", "Simpan", "சேமி", "सहेजें"],
    "Cancel": ["إلغاء", "Annuler", "Cancelar", "Cancelar", "Abbrechen", "取消", "キャンセル", "취소", "Hủy", "Batal", "ரத்துசெய்", "रद्द करें"],
    "Delete": ["حذف", "Supprimer", "Eliminar", "Excluir", "Löschen", "删除", "削除", "삭제", "Xóa", "Padam", "நீக்கு", "हटाएँ"],
    "Edit": ["تعديل", "Modifier", "Editar", "Editar", "Bearbeiten", "编辑", "編集", "편집", "Sửa", "Edit", "திருத்து", "संपादित करें"],
    "Back": ["رجوع", "Retour", "Volver", "Voltar", "Zurück", "返回", "戻る", "뒤로", "Quay lại", "Kembali", "பின்", "वापस"],
    "Yes": ["نعم", "Oui", "Sí", "Sim", "Ja", "是", "はい", "예", "Có", "Ya", "ஆம்", "हाँ"],
    "No": ["لا", "Non", "No", "Não", "Nein", "否", "いいえ", "아니요", "Không", "Tidak", "இல்லை", "नहीं"],
    "New": ["جديد", "Nouveau", "Nuevo", "Novo", "Neu", "新建", "新規", "새로 만들기", "Mới", "Baharu", "புதிய", "नया"],
    "Enabled": ["مفعّل", "Activé", "Activado", "Ativado", "Aktiviert", "已启用", "有効", "사용", "Đã bật", "Didayakan", "இயக்கப்பட்டது", "सक्षम"],
    "Description": ["الوصف", "Description", "Descripción", "Descrição", "Beschreibung", "描述", "説明", "설명", "Mô tả", "Penerangan", "விளக்கம்", "विवरण"],
    "Number": ["الرقم", "Numéro", "Número", "Número", "Nummer", "号码", "番号", "번호", "Số", "Nombor", "எண்", "नंबर"],
    "Agent": ["الوكيل", "Agent", "Agente", "Agente", "Agent", "坐席", "エージェント", "상담원", "Nhân viên", "Ejen", "முகவர்", "एजेंट"],
    "Agents": ["الوكلاء", "Agents", "Agentes", "Agentes", "Agenten", "坐席", "エージェント", "상담원", "Nhân viên", "Ejen", "முகவர்கள்", "एजेंट"],
    "From": ["من", "De", "De", "De", "Von", "来自", "発信元", "발신", "Từ", "Dari", "இருந்து", "से"],
    "To": ["إلى", "À", "A", "Para", "An", "至", "宛先", "수신", "Đến", "Ke", "வரை", "तक"],
    "State": ["الحالة", "État", "Estado", "Estado", "Zustand", "状态", "状態", "상태", "Tình trạng", "Keadaan", "நிலைமை", "अवस्था"],
    "Password": ["كلمة المرور", "Mot de passe", "Contraseña", "Senha", "Passwort", "密码", "パスワード", "비밀번호", "Mật khẩu", "Kata laluan", "கடவுச்சொல்", "पासवर्ड"],
    "Phone settings": ["إعدادات الهاتف", "Réglages du téléphone", "Ajustes del teléfono", "Configurações do telefone", "Telefoneinstellungen", "电话设置", "電話の設定", "전화 설정", "Cài đặt điện thoại", "Tetapan telefon", "தொலைபேசி அமைப்புகள்", "फ़ोन सेटिंग्स"],
    "Show": ["إظهار", "Afficher", "Mostrar", "Mostrar", "Anzeigen", "显示", "表示", "표시", "Hiện", "Tunjuk", "காட்டு", "दिखाएँ"],
    "Hide": ["إخفاء", "Masquer", "Ocultar", "Ocultar", "Verbergen", "隐藏", "非表示", "숨기기", "Ẩn", "Sembunyi", "மறை", "छिपाएँ"],
    "Not available": ["غير متاح", "Non disponible", "No disponible", "Indisponível", "Nicht verfügbar", "不可用", "利用できません", "사용할 수 없음", "Không khả dụng", "Tidak tersedia", "கிடைக்கவில்லை", "उपलब्ध नहीं"],
    "Your account cannot open this page.": ["لا يمكن لحسابك فتح هذه الصفحة.", "Votre compte ne peut pas ouvrir cette page.", "Su cuenta no puede abrir esta página.", "Sua conta não pode abrir esta página.", "Ihr Konto kann diese Seite nicht öffnen.", "您的账户无法打开此页面。", "このページを開く権限がありません。", "계정으로 이 페이지를 열 수 없습니다.", "Tài khoản của bạn không thể mở trang này.", "Akaun anda tidak boleh membuka halaman ini.", "உங்கள் கணக்கால் இந்தப் பக்கத்தைத் திறக்க முடியாது.", "आपका खाता यह पेज नहीं खोल सकता।"],
    "Nothing here yet.": ["لا يوجد شيء هنا بعد.", "Rien pour l'instant.", "Aún no hay nada aquí.", "Nada aqui ainda.", "Noch nichts vorhanden.", "暂无内容。", "まだ何もありません。", "아직 항목이 없습니다.", "Chưa có gì ở đây.", "Belum ada apa-apa.", "இங்கே இன்னும் எதுவும் இல்லை.", "अभी यहाँ कुछ नहीं है।"],
    // Dashboard
    "System Health": ["صحة النظام", "État du système", "Estado del sistema", "Saúde do sistema", "Systemzustand", "系统健康", "システムの状態", "시스템 상태", "Tình trạng hệ thống", "Kesihatan sistem", "அமைப்பு நலம்", "सिस्टम स्वास्थ्य"],
    "System Information": ["معلومات النظام", "Informations système", "Información del sistema", "Informações do sistema", "Systeminformationen", "系统信息", "システム情報", "시스템 정보", "Thông tin hệ thống", "Maklumat sistem", "அமைப்புத் தகவல்", "सिस्टम जानकारी"],
    "Trunks": ["الخطوط", "Trunks", "Troncales", "Troncos", "Trunks", "中继", "トランク", "트렁크", "Trung kế", "Trunk", "டிரங்குகள்", "ट्रंक"],
    "Recent calls": ["المكالمات الأخيرة", "Appels récents", "Llamadas recientes", "Chamadas recentes", "Letzte Anrufe", "最近通话", "最近の通話", "최근 통화", "Cuộc gọi gần đây", "Panggilan terkini", "சமீபத்திய அழைப்புகள்", "हाल की कॉल"],
    "Devices online": ["الأجهزة المتصلة", "Appareils en ligne", "Dispositivos en línea", "Dispositivos online", "Geräte online", "在线设备", "オンラインのデバイス", "온라인 장치", "Thiết bị trực tuyến", "Peranti dalam talian", "இணைப்பிலுள்ள சாதனங்கள்", "ऑनलाइन डिवाइस"],
    "Parked calls": ["المكالمات المعلقة", "Appels parqués", "Llamadas aparcadas", "Chamadas estacionadas", "Geparkte Anrufe", "驻留通话", "パーク中の通話", "파킹된 통화", "Cuộc gọi đang giữ", "Panggilan diletak", "நிறுத்திய அழைப்புகள்", "पार्क की गई कॉल"],
    // Global settings
    "Global Settings": ["الإعدادات العامة", "Paramètres généraux", "Configuración global", "Configurações globais", "Globale Einstellungen", "全局设置", "グローバル設定", "전역 설정", "Cài đặt chung", "Tetapan global", "பொது அமைப்புகள்", "ग्लोबल सेटिंग्स"],
    "Setup": ["الإعداد", "Configuration", "Configuración", "Configuração", "Einrichtung", "设置", "セットアップ", "설정", "Thiết lập", "Persediaan", "அமைவு", "सेटअप"],
    "Tools": ["الأدوات", "Outils", "Herramientas", "Ferramentas", "Werkzeuge", "工具", "ツール", "도구", "Công cụ", "Alatan", "கருவிகள்", "उपकरण"],
    "Troubleshooting": ["استكشاف الأخطاء", "Dépannage", "Solución de problemas", "Solução de problemas", "Fehlerbehebung", "故障排除", "トラブルシューティング", "문제 해결", "Khắc phục sự cố", "Penyelesaian masalah", "சரிசெய்தல்", "समस्या निवारण"],
    "Advanced": ["متقدم", "Avancé", "Avanzado", "Avançado", "Erweitert", "高级", "詳細設定", "고급", "Nâng cao", "Lanjutan", "மேம்பட்டது", "उन्नत"],
    "Music On Hold": ["موسيقى الانتظار", "Musique d'attente", "Música en espera", "Música de espera", "Wartemusik", "等待音乐", "保留音", "대기 음악", "Nhạc chờ", "Muzik menunggu", "காத்திருப்பு இசை", "होल्ड संगीत"],
    "Service Restart": ["إعادة تشغيل الخدمة", "Redémarrage du service", "Reinicio del servicio", "Reiniciar serviço", "Dienst neu starten", "重启服务", "サービス再起動", "서비스 재시작", "Khởi động lại dịch vụ", "Mula semula perkhidmatan", "சேவை மறுதொடக்கம்", "सेवा पुनः आरंभ"],
    "Network": ["الشبكة", "Réseau", "Red", "Rede", "Netzwerk", "网络", "ネットワーク", "네트워크", "Mạng", "Rangkaian", "வலையமைப்பு", "नेटवर्क"],
    "SIP Profile": ["ملف SIP", "Profil SIP", "Perfil SIP", "Perfil SIP", "SIP-Profil", "SIP 配置文件", "SIP プロファイル", "SIP 프로필", "Hồ sơ SIP", "Profil SIP", "SIP சுயவிவரம்", "SIP प्रोफ़ाइल"],
    "Dialplans": ["خطط الاتصال", "Plans de numérotation", "Planes de marcado", "Planos de discagem", "Wählpläne", "拨号计划", "ダイヤルプラン", "다이얼 플랜", "Kế hoạch quay số", "Pelan dail", "டயல் திட்டங்கள்", "डायलप्लान"],
    "Gateways": ["البوابات", "Passerelles", "Pasarelas", "Gateways", "Gateways", "网关", "ゲートウェイ", "게이트웨이", "Cổng", "Get laluan", "நுழைவாயில்கள்", "गेटवे"],
    "Audit Logs": ["سجلات التدقيق", "Journaux d'audit", "Registros de auditoría", "Logs de auditoria", "Prüfprotokolle", "审计日志", "監査ログ", "감사 로그", "Nhật ký kiểm tra", "Log audit", "தணிக்கைப் பதிவுகள்", "ऑडिट लॉग"],
    "Call Landing": ["استقبال المكالمات", "Arrivée des appels", "Llegada de llamadas", "Entrada de chamadas", "Anrufannahme", "来电落地", "着信先", "착신 설정", "Điểm nhận cuộc gọi", "Pendaratan panggilan", "அழைப்பு வருகை", "कॉल लैंडिंग"],
    // Call Flow and the hotel front desk
    "Call Flow": ["تدفق المكالمات", "Flux d'appels", "Flujo de llamadas", "Fluxo de chamadas", "Anrufablauf", "呼叫流程", "コールフロー", "통화 흐름", "Luồng cuộc gọi", "Aliran panggilan", "அழைப்பு ஓட்டம்", "कॉल फ़्लो"],
    "Hotel front desk": ["مكتب استقبال الفندق", "Réception de l'hôtel", "Recepción del hotel", "Recepção do hotel", "Hotelrezeption", "酒店前台", "ホテルフロント", "호텔 프런트", "Lễ tân khách sạn", "Kaunter hadapan hotel", "ஹோட்டல் வரவேற்பு", "होटल फ्रंट डेस्क"],
    "Available DID Numbers": ["أرقام DID المتاحة", "Numéros SDA disponibles", "Números DID disponibles", "Números DID disponíveis", "Verfügbare DID-Nummern", "可用 DID 号码", "利用可能な DID 番号", "사용 가능한 DID 번호", "Số DID khả dụng", "Nombor DID tersedia", "கிடைக்கும் DID எண்கள்", "उपलब्ध DID नंबर"],
    "Interactive Voice Response": ["الرد الصوتي التفاعلي", "Serveur vocal interactif", "Respuesta de voz interactiva", "Resposta de voz interativa", "Sprachdialogsystem (IVR)", "交互式语音应答", "自動音声応答", "대화형 음성 응답", "Trả lời thoại tương tác", "Respons suara interaktif", "ஊடாடும் குரல் பதில்", "इंटरैक्टिव वॉइस रिस्पॉन्स"],
    "Others": ["أخرى", "Autres", "Otros", "Outros", "Sonstige", "其他", "その他", "기타", "Khác", "Lain-lain", "மற்றவை", "अन्य"],
    "SIP Trunk": ["خط SIP", "Trunk SIP", "Troncal SIP", "Tronco SIP", "SIP-Trunk", "SIP 中继", "SIP トランク", "SIP 트렁크", "Trung kế SIP", "Trunk SIP", "SIP டிரங்க்", "SIP ट्रंक"],
    "Gateway": ["بوابة", "Passerelle", "Pasarela", "Gateway", "Gateway", "网关", "ゲートウェイ", "게이트웨이", "Cổng", "Get laluan", "நுழைவாயில்", "गेटवे"],
    "DID": ["DID", "SDA", "DID", "DID", "DID", "DID", "DID", "DID", "DID", "DID", "DID", "DID"],
    "DID Number": ["رقم DID", "Numéro SDA", "Número DID", "Número DID", "DID-Nummer", "DID 号码", "DID 番号", "DID 번호", "Số DID", "Nombor DID", "DID எண்", "DID नंबर"],
    "Trunk": ["الخط", "Trunk", "Troncal", "Tronco", "Trunk", "中继", "トランク", "트렁크", "Trung kế", "Trunk", "டிரங்க்", "ट्रंक"],
    "Country": ["الدولة", "Pays", "País", "País", "Land", "国家", "国", "국가", "Quốc gia", "Negara", "நாடு", "देश"],
    "Goes to": ["يذهب إلى", "Va vers", "Va a", "Vai para", "Geht an", "转到", "転送先", "연결 대상", "Chuyển đến", "Pergi ke", "செல்லும் இடம்", "जाता है"],
    "Not assigned": ["غير مخصص", "Non attribué", "Sin asignar", "Não atribuído", "Nicht zugewiesen", "未分配", "未割り当て", "할당 안 됨", "Chưa gán", "Belum ditetapkan", "ஒதுக்கப்படவில்லை", "असाइन नहीं"],
    "Assign": ["تخصيص", "Attribuer", "Asignar", "Atribuir", "Zuweisen", "分配", "割り当て", "할당", "Gán", "Tetapkan", "ஒதுக்கு", "असाइन करें"],
    "Assigned": ["تم التخصيص", "Attribué", "Asignado", "Atribuído", "Zugewiesen", "已分配", "割り当て済み", "할당됨", "Đã gán", "Ditetapkan", "ஒதுக்கப்பட்டது", "असाइन किया गया"],
    "Customize": ["تخصيص", "Personnaliser", "Personalizar", "Personalizar", "Anpassen", "自定义", "カスタマイズ", "사용자 지정", "Tùy chỉnh", "Sesuaikan", "தனிப்பயனாக்கு", "अनुकूलित करें"],
    "View 1": ["العرض 1", "Vue 1", "Vista 1", "Visão 1", "Ansicht 1", "视图 1", "ビュー 1", "보기 1", "Chế độ xem 1", "Paparan 1", "காட்சி 1", "दृश्य 1"],
    "View 2": ["العرض 2", "Vue 2", "Vista 2", "Visão 2", "Ansicht 2", "视图 2", "ビュー 2", "보기 2", "Chế độ xem 2", "Paparan 2", "காட்சி 2", "दृश्य 2"],
    "Enable": ["تفعيل", "Activer", "Activar", "Ativar", "Aktivieren", "启用", "有効", "사용", "Bật", "Dayakan", "இயக்கு", "सक्षम"],
    "Register": ["تسجيل", "Enregistrement", "Registro", "Registro", "Registrierung", "注册", "登録", "등록", "Đăng ký", "Daftar", "பதிவு", "पंजीकरण"],
    "Expand": ["توسيع", "Agrandir", "Ampliar", "Expandir", "Erweitern", "展开", "拡大", "확장", "Mở rộng", "Kembangkan", "விரிவாக்கு", "विस्तार करें"],
    "Export CSV": ["تصدير CSV", "Exporter en CSV", "Exportar CSV", "Exportar CSV", "CSV exportieren", "导出 CSV", "CSV エクスポート", "CSV 내보내기", "Xuất CSV", "Eksport CSV", "CSV ஏற்றுமதி", "CSV निर्यात"],
    "IVR Modules": ["وحدات IVR", "Modules SVI", "Módulos IVR", "Módulos URA", "IVR-Module", "IVR 模块", "IVR モジュール", "IVR 모듈", "Mô-đun IVR", "Modul IVR", "IVR தொகுதிகள்", "IVR मॉड्यूल"],
    "Existing Queues": ["قوائم الانتظار الحالية", "Files existantes", "Colas existentes", "Filas existentes", "Vorhandene Warteschlangen", "现有队列", "既存のキュー", "기존 대기열", "Hàng đợi hiện có", "Baris gilir sedia ada", "உள்ள வரிசைகள்", "मौजूदा कतारें"],
    "Existing Extensions": ["الأرقام الداخلية الحالية", "Postes existants", "Extensiones existentes", "Ramais existentes", "Vorhandene Nebenstellen", "现有分机", "既存の内線", "기존 내선", "Máy nhánh hiện có", "Sambungan sedia ada", "உள்ள நீட்டிப்புகள்", "मौजूदा एक्सटेंशन"],
    "Any extension (receives faxes)": ["أي تحويلة (تستقبل الفاكسات)", "N'importe quel poste (reçoit les fax)", "Cualquier extensión (recibe faxes)", "Qualquer ramal (recebe faxes)", "Jede Nebenstelle (empfängt Faxe)", "任意分机（接收传真）", "任意の内線（FAX受信）", "모든 내선 (팩스 수신)", "Bất kỳ máy nhánh nào (nhận fax)", "Mana-mana sambungan (menerima faks)", "எந்த நீட்டிப்பும் (தொலைநகல் பெறும்)", "कोई भी एक्सटेंशन (फ़ैक्स प्राप्त करता है)"],
    "Call transcription": ["تفريغ المكالمات", "Transcription des appels", "Transcripción de llamadas", "Transcrição de chamadas", "Anruftranskription", "通话转写", "通話の文字起こし", "통화 녹취록", "Chép lời cuộc gọi", "Transkripsi panggilan", "அழைப்பு எழுத்துப்படி", "कॉल ट्रांसक्रिप्शन"],
    "Call Transcription": ["تفريغ المكالمات", "Transcription des appels", "Transcripción de llamadas", "Transcrição de chamadas", "Anruftranskription", "通话转写", "通話の文字起こし", "통화 녹취록", "Chép lời cuộc gọi", "Transkripsi panggilan", "அழைப்பு எழுத்துப்படி", "कॉल ट्रांसक्रिप्शन"],
    "Transcript": ["النص", "Transcription", "Transcripción", "Transcrição", "Transkript", "转写", "文字起こし", "녹취록", "Bản ghi lời", "Transkrip", "எழுத்துப்படி", "ट्रांसक्रिप्ट"],
    "Sentiment": ["المشاعر", "Sentiment", "Sentimiento", "Sentimento", "Stimmung", "情绪", "感情", "감정", "Cảm xúc", "Sentimen", "உணர்வு", "भावना"],
    "Call transcript": ["نص المكالمة", "Transcription de l'appel", "Transcripción de la llamada", "Transcrição da chamada", "Anruftranskript", "通话转写", "通話の文字起こし", "통화 녹취록", "Bản ghi cuộc gọi", "Transkrip panggilan", "அழைப்பு எழுத்துப்படி", "कॉल ट्रांसक्रिप्ट"],
    "Existing Modules": ["الوحدات الحالية", "Modules existants", "Módulos existentes", "Módulos existentes", "Vorhandene Module", "现有模块", "既存のモジュール", "기존 모듈", "Mô-đun hiện có", "Modul sedia ada", "உள்ள தொகுதிகள்", "मौजूदा मॉड्यूल"],
    "All rooms": ["كل الغرف", "Toutes les chambres", "Todas las habitaciones", "Todos os quartos", "Alle Zimmer", "所有房间", "全客室", "모든 객실", "Tất cả phòng", "Semua bilik", "அனைத்து அறைகள்", "सभी कमरे"],
    "Occupied": ["مشغولة", "Occupée", "Ocupada", "Ocupado", "Belegt", "已入住", "使用中", "투숙 중", "Có khách", "Berpenghuni", "நிரம்பியது", "भरा हुआ"],
    "Vacant": ["شاغرة", "Libre", "Libre", "Vago", "Frei", "空房", "空室", "공실", "Trống", "Kosong", "காலி", "खाली"],
    "Room": ["الغرفة", "Chambre", "Habitación", "Quarto", "Zimmer", "房间", "客室", "객실", "Phòng", "Bilik", "அறை", "कमरा"],
    "Guest": ["النزيل", "Client", "Huésped", "Hóspede", "Gast", "住客", "宿泊客", "투숙객", "Khách", "Tetamu", "விருந்தினர்", "अतिथि"],
    "Check in": ["تسجيل الوصول", "Enregistrer l'arrivée", "Registrar entrada", "Fazer check-in", "Einchecken", "入住", "チェックイン", "체크인", "Nhận phòng", "Daftar masuk", "செக்-இன்", "चेक-इन"],
    "Check out": ["تسجيل المغادرة", "Enregistrer le départ", "Registrar salida", "Fazer check-out", "Auschecken", "退房", "チェックアウト", "체크아웃", "Trả phòng", "Daftar keluar", "செக்-அவுட்", "चेक-आउट"],
    "Checked in": ["وصل", "Arrivé", "Entrada", "Check-in", "Eingecheckt", "入住于", "チェックイン", "체크인", "Đã nhận phòng", "Daftar masuk", "செக்-இன் ஆனது", "चेक-इन"],
    "Check in a guest": ["تسجيل وصول نزيل", "Enregistrer un client", "Registrar un huésped", "Fazer check-in de um hóspede", "Gast einchecken", "办理入住", "宿泊客をチェックイン", "투숙객 체크인", "Nhận phòng cho khách", "Daftar masuk tetamu", "விருந்தினரை செக்-இன் செய்", "अतिथि का चेक-इन करें"],
    "Edit guest": ["تعديل النزيل", "Modifier le client", "Editar huésped", "Editar hóspede", "Gast bearbeiten", "编辑住客", "宿泊客を編集", "투숙객 편집", "Sửa thông tin khách", "Edit tetamu", "விருந்தினரைத் திருத்து", "अतिथि संपादित करें"],
    "Guest first name": ["الاسم الأول للنزيل", "Prénom du client", "Nombre del huésped", "Nome do hóspede", "Vorname des Gastes", "住客名字", "宿泊客の名", "투숙객 이름", "Tên khách", "Nama pertama tetamu", "விருந்தினர் முதல் பெயர்", "अतिथि का पहला नाम"],
    "Guest last name": ["اسم عائلة النزيل", "Nom du client", "Apellido del huésped", "Sobrenome do hóspede", "Nachname des Gastes", "住客姓氏", "宿泊客の姓", "투숙객 성", "Họ của khách", "Nama akhir tetamu", "விருந்தினர் கடைசி பெயர்", "अतिथि का उपनाम"],
    "Wake-up call": ["مكالمة الإيقاظ", "Appel de réveil", "Llamada de despertador", "Chamada de despertar", "Weckruf", "叫醒电话", "モーニングコール", "모닝콜", "Cuộc gọi báo thức", "Panggilan bangun", "எழுப்பும் அழைப்பு", "वेक-अप कॉल"],
    "Schedule wake-up": ["جدولة الإيقاظ", "Programmer le réveil", "Programar despertador", "Agendar despertar", "Weckruf planen", "预约叫醒", "モーニングコールを予約", "모닝콜 예약", "Đặt báo thức", "Jadualkan panggilan bangun", "எழுப்புதலை திட்டமிடு", "वेक-अप शेड्यूल करें"],
    "Cancel wake-up": ["إلغاء الإيقاظ", "Annuler le réveil", "Cancelar despertador", "Cancelar despertar", "Weckruf absagen", "取消叫醒", "モーニングコールを取消", "모닝콜 취소", "Hủy báo thức", "Batal panggilan bangun", "எழுப்புதலை ரத்து செய்", "वेक-अप रद्द करें"],
    "Wake-up sound": ["صوت الإيقاظ", "Son du réveil", "Sonido del despertador", "Som do despertar", "Weckton", "叫醒铃声", "モーニングコールの音", "모닝콜 소리", "Âm báo thức", "Bunyi panggilan bangun", "எழுப்பும் ஒலி", "वेक-अप ध्वनि"],
    "Scheduled": ["مجدولة", "Programmé", "Programada", "Agendado", "Geplant", "已预约", "予約済み", "예약됨", "Đã lên lịch", "Dijadualkan", "திட்டமிடப்பட்டது", "शेड्यूल्ड"],
    "Not answered": ["لم يتم الرد", "Sans réponse", "Sin respuesta", "Não atendida", "Nicht angenommen", "未接听", "応答なし", "응답 없음", "Không trả lời", "Tidak dijawab", "பதிலளிக்கப்படவில்லை", "उत्तर नहीं दिया"],
    "Today": ["اليوم", "Aujourd'hui", "Hoy", "Hoje", "Heute", "今天", "今日", "오늘", "Hôm nay", "Hari ini", "இன்று", "आज"],
    "Tomorrow": ["غدًا", "Demain", "Mañana", "Amanhã", "Morgen", "明天", "明日", "내일", "Ngày mai", "Esok", "நாளை", "कल"],
    "Date": ["التاريخ", "Date", "Fecha", "Data", "Datum", "日期", "日付", "날짜", "Ngày", "Tarikh", "தேதி", "तारीख"],
    "Change": ["تغيير", "Modifier", "Cambiar", "Alterar", "Ändern", "更改", "変更", "변경", "Thay đổi", "Tukar", "மாற்று", "बदलें"],
    "Phone": ["الهاتف", "Téléphone", "Teléfono", "Telefone", "Telefon", "电话", "電話", "전화", "Điện thoại", "Telefon", "தொலைபேசி", "फ़ोन"],
    "Search rooms or guests": ["ابحث في الغرف أو النزلاء", "Rechercher chambres ou clients", "Buscar habitaciones o huéspedes", "Pesquisar quartos ou hóspedes", "Zimmer oder Gäste suchen", "搜索房间或住客", "客室・宿泊客を検索", "객실 또는 투숙객 검색", "Tìm phòng hoặc khách", "Cari bilik atau tetamu", "அறைகள் அல்லது விருந்தினர்களைத் தேடு", "कमरे या अतिथि खोजें"],
    "Move back": ["إعادة", "Remettre", "Devolver", "Devolver", "Zurücklegen", "移回", "戻す", "되돌리기", "Chuyển lại", "Pindah balik", "திருப்பி நகர்த்து", "वापस ले जाएँ"],
    "Back to Available DID Numbers": ["إعادة إلى أرقام DID المتاحة", "Retour aux numéros SDA disponibles", "Volver a números DID disponibles", "Voltar aos números DID disponíveis", "Zurück zu verfügbaren DID-Nummern", "移回可用 DID 号码", "利用可能な DID 番号に戻す", "사용 가능한 DID 번호로 되돌리기", "Trả về số DID khả dụng", "Kembali ke nombor DID tersedia", "கிடைக்கும் DID எண்களுக்குத் திரும்பு", "उपलब्ध DID नंबर पर वापस"],
    "Drop here to make the number available again": ["أفلت هنا لجعل الرقم متاحًا مجددًا", "Déposez ici pour rendre le numéro disponible", "Suelte aquí para dejar el número disponible", "Solte aqui para deixar o número disponível", "Hier ablegen, um die Nummer freizugeben", "拖到这里使号码重新可用", "ここにドロップして番号を空きに戻す", "여기에 놓으면 번호를 다시 사용할 수 있습니다", "Thả vào đây để trả số về khả dụng", "Lepaskan di sini untuk menjadikan nombor tersedia semula", "எண்ணை மீண்டும் கிடைக்கச் செய்ய இங்கே விடவும்", "नंबर फिर से उपलब्ध करने के लिए यहाँ छोड़ें"],
    // Integrations, single sign-on
    "Integrations": ["التكاملات", "Intégrations", "Integraciones", "Integrações", "Integrationen", "集成", "連携", "통합", "Tích hợp", "Integrasi", "ஒருங்கிணைப்புகள்", "इंटीग्रेशन"],
    "Single sign-on": ["تسجيل الدخول الموحد", "Authentification unique", "Inicio de sesión único", "Login único", "Single Sign-On", "单点登录", "シングルサインオン", "통합 로그인", "Đăng nhập một lần", "Log masuk tunggal", "ஒற்றை உள்நுழைவு", "सिंगल साइन-ऑन"],
    "Sign-in options": ["خيارات تسجيل الدخول", "Options de connexion", "Opciones de inicio de sesión", "Opções de login", "Anmeldeoptionen", "登录选项", "サインインオプション", "로그인 옵션", "Tùy chọn đăng nhập", "Pilihan log masuk", "உள்நுழைவு விருப்பங்கள்", "साइन-इन विकल्प"],
    "Configure": ["تهيئة", "Configurer", "Configurar", "Configurar", "Konfigurieren", "配置", "設定", "구성", "Cấu hình", "Konfigurasi", "கட்டமை", "कॉन्फ़िगर करें"],
    "Connect": ["اتصال", "Connecter", "Conectar", "Conectar", "Verbinden", "连接", "接続", "연결", "Kết nối", "Sambung", "இணை", "कनेक्ट करें"],
    "Enable SSO": ["تفعيل الدخول الموحد", "Activer le SSO", "Activar SSO", "Ativar SSO", "SSO aktivieren", "启用 SSO", "SSO を有効化", "SSO 사용", "Bật SSO", "Dayakan SSO", "SSO இயக்கு", "SSO सक्षम करें"],
    "Client ID": ["معرّف العميل", "ID client", "ID de cliente", "ID do cliente", "Client-ID", "客户端 ID", "クライアント ID", "클라이언트 ID", "ID ứng dụng khách", "ID klien", "கிளையன்ட் ID", "क्लाइंट ID"],
    "Client secret": ["سر العميل", "Secret client", "Secreto de cliente", "Segredo do cliente", "Client-Geheimnis", "客户端密钥", "クライアントシークレット", "클라이언트 암호", "Mã bí mật ứng dụng", "Rahsia klien", "கிளையன்ட் ரகசியம்", "क्लाइंट सीक्रेट"],
    "Tenant ID": ["معرّف المستأجر", "ID du locataire", "ID de inquilino", "ID do locatário", "Mandanten-ID", "租户 ID", "テナント ID", "테넌트 ID", "ID tenant", "ID penyewa", "டெனன்ட் ID", "टेनेंट ID"],
    "Redirect URI": ["عنوان إعادة التوجيه", "URI de redirection", "URI de redirección", "URI de redirecionamento", "Weiterleitungs-URI", "重定向 URI", "リダイレクト URI", "리디렉션 URI", "URI chuyển hướng", "URI ubah hala", "திருப்பி அனுப்பும் URI", "रीडायरेक्ट URI"],
    "Setup instructions": ["تعليمات الإعداد", "Instructions de configuration", "Instrucciones de configuración", "Instruções de configuração", "Einrichtungsanleitung", "设置说明", "設定手順", "설정 안내", "Hướng dẫn cài đặt", "Arahan persediaan", "அமைவு வழிமுறைகள்", "सेटअप निर्देश"],
    "App credentials": ["بيانات اعتماد التطبيق", "Identifiants de l'application", "Credenciales de la app", "Credenciais do app", "App-Zugangsdaten", "应用凭据", "アプリの資格情報", "앱 자격 증명", "Thông tin xác thực ứng dụng", "Kelayakan aplikasi", "செயலி சான்றுகள்", "ऐप क्रेडेंशियल"],
    "API credentials": ["بيانات اعتماد API", "Identifiants API", "Credenciales de API", "Credenciais da API", "API-Zugangsdaten", "API 凭据", "API 資格情報", "API 자격 증명", "Thông tin xác thực API", "Kelayakan API", "API சான்றுகள்", "API क्रेडेंशियल"],
    "Save configuration": ["حفظ الإعدادات", "Enregistrer la configuration", "Guardar configuración", "Salvar configuração", "Konfiguration speichern", "保存配置", "設定を保存", "구성 저장", "Lưu cấu hình", "Simpan konfigurasi", "கட்டமைப்பைச் சேமி", "कॉन्फ़िगरेशन सहेजें"],
    "Connect LinkedIn": ["ربط LinkedIn", "Connecter LinkedIn", "Conectar LinkedIn", "Conectar LinkedIn", "LinkedIn verbinden", "连接 LinkedIn", "LinkedIn を接続", "LinkedIn 연결", "Kết nối LinkedIn", "Sambung LinkedIn", "LinkedIn இணை", "LinkedIn कनेक्ट करें"],
    "Built-in": ["مدمج", "Intégré", "Integrado", "Integrado", "Integriert", "内置", "内蔵", "기본 제공", "Tích hợp sẵn", "Terbina dalam", "உள்ளமைந்த", "बिल्ट-इन"],
    "Options": ["خيارات", "Options", "Opciones", "Opções", "Optionen", "选项", "オプション", "옵션", "Tùy chọn", "Pilihan", "விருப்பங்கள்", "विकल्प"],
    "Sign in with": ["تسجيل الدخول باستخدام", "Se connecter avec", "Iniciar sesión con", "Entrar com", "Anmelden mit", "登录方式：", "でサインイン:", "로그인:", "Đăng nhập bằng", "Log masuk dengan", "இதன் மூலம் உள்நுழை:", "इससे साइन इन करें:"],
    "or": ["أو", "ou", "o", "ou", "oder", "或", "または", "또는", "hoặc", "atau", "அல்லது", "या"],
    "Copied": ["تم النسخ", "Copié", "Copiado", "Copiado", "Kopiert", "已复制", "コピーしました", "복사됨", "Đã sao chép", "Disalin", "நகலெடுக்கப்பட்டது", "कॉपी किया गया"],
    "Remove": ["إزالة", "Retirer", "Quitar", "Remover", "Entfernen", "移除", "削除", "제거", "Gỡ bỏ", "Alih keluar", "அகற்று", "हटाएँ"],
    "Removed": ["تمت الإزالة", "Retiré", "Quitado", "Removido", "Entfernt", "已移除", "削除しました", "제거됨", "Đã gỡ bỏ", "Dialih keluar", "அகற்றப்பட்டது", "हटा दिया गया"],
    "Off": ["متوقف", "Désactivé", "Desactivado", "Desligado", "Aus", "关闭", "オフ", "꺼짐", "Tắt", "Mati", "அணைப்பு", "बंद"],
    "Sign-in email (single sign-on)": ["بريد تسجيل الدخول (الدخول الموحد)", "E-mail de connexion (SSO)", "Correo de inicio de sesión (SSO)", "E-mail de login (SSO)", "Anmelde-E-Mail (SSO)", "登录邮箱（单点登录）", "サインイン用メール（SSO）", "로그인 이메일(SSO)", "Email đăng nhập (SSO)", "E-mel log masuk (SSO)", "உள்நுழைவு மின்னஞ்சல் (SSO)", "साइन-इन ईमेल (SSO)"],
    "Company domain": ["نطاق الشركة", "Domaine de l'entreprise", "Dominio de la empresa", "Domínio da empresa", "Firmendomäne", "公司域名", "会社のドメイン", "회사 도메인", "Tên miền công ty", "Domain syarikat", "நிறுவன டொமைன்", "कंपनी डोमेन"],
  };
  // The same words with other capitals.
  const ALIAS = {
    'Active Calls': 'Active calls', 'Contact Center': 'Contact center', 'Outbound Rules': 'Outbound rules', 'Office Hours': 'Office hours',
    'Aux Codes': 'AUX / break codes', 'User Roles': 'User roles', 'Call History': 'Call history', 'Call Centre (live)': 'Call centre (live)',
    'Call centre': 'Call centre (live)', 'Sign out': 'Logout', 'Hangup': 'Hang up',
  };

  let code = 'en';
  let idx = -1;
  try { code = localStorage.getItem('rq-lang') || 'en'; } catch (e) { /* private mode */ }
  if (!LANGS.some((l) => l[0] === code)) code = 'en';
  idx = ORDER.indexOf(code);

  function tr(s) {
    if (idx < 0 || typeof s !== 'string' || !s) return s;
    const e = D[s] || D[ALIAS[s]];
    if (e) return e[idx] || s;
    const m = /^(\s*)(.*?)(\s*)$/s.exec(s); // "Global Settings " and the like
    if (m && (m[1] || m[3])) {
      const c = D[m[2]] || D[ALIAS[m[2]]];
      if (c && c[idx]) return m[1] + c[idx] + m[3];
    }
    return s;
  }

  // The page's fixed texts (sign-in page, top bar): translated from the
  // English kept in data-en.
  function applyStatic() {
    document.documentElement.lang = code;
    document.documentElement.dir = code === 'ar' ? 'rtl' : 'ltr';
    document.querySelectorAll('[data-i18n]').forEach((el) => {
      const node = [...el.childNodes].find((n) => n.nodeType === 3 && n.textContent.trim());
      if (!node) return;
      if (el.dataset.en === undefined) el.dataset.en = node.textContent;
      node.textContent = tr(el.dataset.en);
    });
    document.querySelectorAll('[data-i18n-attr]').forEach((el) => {
      for (const a of el.dataset.i18nAttr.split(',')) {
        const key = 'en' + a.replace(/[^a-z]/gi, '');
        if (el.dataset[key] === undefined) el.dataset[key] = el.getAttribute(a) || '';
        el.setAttribute(a, tr(el.dataset[key]));
      }
    });
  }

  function set(c) {
    code = LANGS.some((l) => l[0] === c) ? c : 'en';
    idx = ORDER.indexOf(code);
    try { localStorage.setItem('rq-lang', code); } catch (e) { /* private mode */ }
    applyStatic();
    window.dispatchEvent(new Event('rq-lang'));
  }

  return { LANGS, tr, set, lang: () => code, applyStatic };
})();
const t = (s) => I18N.tr(s);
