'use strict';
/*
 * JASQL app shell: fixed left sidebar + top-right profile menu.
 *
 * Every console page includes <script src="/admin/nav.js"></script> and calls
 *   JASQL.mount({ page: 'ask', onReady(me){ ... } })
 * where `page` is the current nav key (for highlighting) and onReady fires
 * after auth resolves with the /auth/me payload.
 *
 * The shell injects a left sidebar (grouped nav, current-tab highlight,
 * responsive) and shifts the page body to its right. Each page keeps its own
 * <header> as the top bar; the shell appends a profile button on the right that
 * opens a dropdown (개인정보/비밀번호/키 관리/로그아웃 + version footer). The legacy
 * master-token box is hidden by default behind a small toggle in standalone mode.
 */
(function () {
  var SBW = 224; // sidebar width (px)

  // nav groups. show: always | auth (meta DB) | admin
  var GROUPS = [
    { title: '작업', items: [
      { key: 'ask',      href: '/admin/ask',     icon: '💬', label: '질의',      show: 'always' },
      { key: 'history',  href: '/admin/history', icon: '🕘', label: '내 이력',   show: 'auth' },
      { key: 'stats',    href: '/admin/stats',   icon: '📊', label: '통계',      show: 'auth' },
      { key: 'datasets', href: '/admin',         icon: '🗂', label: '데이터셋',  show: 'always' },
      { key: 'editor',   href: '/admin/editor',  icon: '✏️', label: '테이블 편집', show: 'always' },
      { key: 'db',       href: '/admin/db',      icon: '🔌', label: 'DB · 쿼리',  show: 'always' },
      { key: 'reviews',  href: '/admin/reviews', icon: '🧾', label: '메타 검토',  show: 'always' },
    ]},
    { title: '관리', items: [
      { key: 'quality',  href: '/admin/quality',  icon: '📈', label: '메타 품질', show: 'always' },
      { key: 'users',    href: '/admin/users',    icon: '👥', label: '사용자',   show: 'admin' },
      { key: 'settings', href: '/admin/settings', icon: '⚙️', label: '서버 설정', show: 'admin' },
      { key: 'keys',     href: '/admin/keys',     icon: '🔑', label: 'MCP 키',   show: 'auth' },
    ]},
    { title: '문서', items: [
      { key: 'docs',     href: '/docs',   icon: '📘', label: 'API 문서', show: 'always', target: '_blank' },
    ]},
  ];

  var esc = function (s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); };

  function injectStyles() {
    if (document.getElementById('jasql-shell-style')) return;
    var st = document.createElement('style');
    st.id = 'jasql-shell-style';
    st.textContent = [
      ':root{--jsb-w:' + SBW + 'px;--jsb-bg:#0d1626;--jsb-bg2:#111f38;--jsb-ink:#e8eefc;--jsb-sub:#8fa3c4;--jsb-active:#2563eb;}',
      'body{padding-left:var(--jsb-w);transition:padding-left .18s ease;}',
      '@media(max-width:860px){body{padding-left:0;}}',
      // sidebar
      '.jsb{position:fixed;top:0;left:0;width:var(--jsb-w);height:100vh;background:linear-gradient(180deg,#0d1626,#0f1c33);',
      '  color:var(--jsb-ink);display:flex;flex-direction:column;z-index:60;border-right:1px solid #1e2f4d;transition:transform .18s ease;}',
      '.jsb .jbrand{display:flex;align-items:center;gap:9px;padding:16px 18px;font-size:17px;font-weight:800;color:#fff;text-decoration:none;letter-spacing:.3px;}',
      '.jsb .jbrand .dot{font-size:20px;}',
      '.jsb nav{flex:1;overflow-y:auto;padding:6px 10px;}',
      '.jsb .jgrp{margin:10px 4px 4px;font-size:11px;letter-spacing:.6px;text-transform:uppercase;color:var(--jsb-sub);}',
      '.jsb a.jlink{display:flex;align-items:center;gap:10px;padding:9px 12px;margin:2px 0;border-radius:9px;color:#c6d4ec;',
      '  text-decoration:none;font-size:14px;line-height:1.2;}',
      '.jsb a.jlink .ic{width:20px;text-align:center;font-size:15px;}',
      '.jsb a.jlink:hover{background:#17294690;color:#fff;}',
      '.jsb a.jlink.active{background:var(--jsb-active);color:#fff;font-weight:700;box-shadow:0 2px 10px rgba(37,99,235,.4);}',
      '.jsb .jfoot{padding:12px 16px;border-top:1px solid #1e2f4d;font-size:11.5px;color:var(--jsb-sub);}',
      // mobile toggle
      '.jsb-toggle{display:none;position:fixed;top:10px;left:10px;z-index:70;background:#0d1626;color:#fff;border:1px solid #35507f;',
      '  border-radius:8px;padding:7px 11px;font-size:16px;cursor:pointer;}',
      '@media(max-width:860px){.jsb{transform:translateX(-100%);} body.jsb-open .jsb{transform:translateX(0);} .jsb-toggle{display:block;}',
      '  header{padding-left:56px !important;}}',
      '.jsb-scrim{display:none;position:fixed;inset:0;background:rgba(3,8,18,.5);z-index:55;}',
      'body.jsb-open .jsb-scrim{display:block;}',
      // profile menu
      '.jprofile{position:relative;}',
      '.jprofile>button.javatar{display:flex;align-items:center;gap:8px;background:#1d2b47;border:1px solid #35507f;color:#e8eefc;',
      '  border-radius:20px;padding:5px 12px 5px 6px;font-size:13px;cursor:pointer;}',
      '.jprofile>button.javatar:hover{background:#26375a;}',
      '.jprofile .jav{width:26px;height:26px;border-radius:50%;background:#2563eb;color:#fff;display:flex;align-items:center;justify-content:center;font-size:13px;font-weight:700;}',
      '.jmenu{position:absolute;right:0;top:calc(100% + 8px);width:250px;background:#fff;color:#1c2430;border:1px solid #dde4ec;',
      '  border-radius:12px;box-shadow:0 12px 34px rgba(16,26,46,.22);overflow:hidden;display:none;z-index:80;}',
      '.jprofile.open .jmenu{display:block;}',
      '.jmenu .jmhead{padding:14px 16px;background:#f5f7fa;border-bottom:1px solid #eef1f5;}',
      '.jmenu .jmhead b{display:block;font-size:14px;}',
      '.jmenu .jmhead .jmsub{font-size:12px;color:#5b6b7f;margin-top:2px;word-break:break-all;}',
      '.jmenu button.jmitem{display:flex;width:100%;align-items:center;gap:10px;background:none;border:0;text-align:left;',
      '  padding:10px 16px;font-size:13.5px;color:#1c2430;cursor:pointer;}',
      '.jmenu button.jmitem:hover{background:#f2f6ff;}',
      '.jmenu button.jmitem.danger{color:#b91c1c;}',
      '.jmenu .jmsep{height:1px;background:#eef1f5;margin:4px 0;}',
      '.jmenu .jmfoot{padding:9px 16px;border-top:1px solid #eef1f5;font-size:11.5px;color:#8794a8;background:#fafbfc;}',
      // modal
      '.jmodal{position:fixed;inset:0;background:rgba(10,18,32,.5);display:none;align-items:center;justify-content:center;z-index:90;}',
      '.jmodal.open{display:flex;}',
      '.jmodal .box{background:#fff;color:#1c2430;border-radius:14px;width:min(420px,92vw);padding:22px 24px;box-shadow:0 20px 60px rgba(0,0,0,.3);}',
      '.jmodal h3{margin:0 0 4px;font-size:17px;}',
      '.jmodal p.sub{margin:0 0 14px;font-size:12.5px;color:#5b6b7f;}',
      '.jmodal label{display:block;font-size:12.5px;color:#5b6b7f;margin:10px 0 3px;}',
      '.jmodal input{width:100%;border:1px solid #dde4ec;border-radius:8px;padding:9px 11px;font-size:14px;box-sizing:border-box;}',
      '.jmodal .acts{display:flex;justify-content:flex-end;gap:8px;margin-top:18px;}',
      '.jmodal .jb{border:1px solid #dde4ec;background:#fff;border-radius:8px;padding:8px 15px;font-size:13.5px;cursor:pointer;}',
      '.jmodal .jb.primary{background:#2563eb;border-color:#2563eb;color:#fff;font-weight:600;}',
      '.jmodal .msg{font-size:12.5px;margin-top:10px;min-height:16px;}',
      '.jmodal .msg.ok{color:#15803d;} .jmodal .msg.bad{color:#b91c1c;}',
    ].join('\n');
    document.head.appendChild(st);
  }

  function tokenHeaders(json) {
    var h = {};
    if (json) h['Content-Type'] = 'application/json';
    var tok = document.getElementById('adminToken');
    if (tok && tok.value) h['X-Admin-Token'] = tok.value;
    return h;
  }

  function buildSidebar(me) {
    var authed = !!(me && me.auth_enabled);
    var admin = authed && me.authenticated && me.user && me.user.role === 'admin';
    var cur = window.JASQL.page;
    var show = function (rule) {
      return rule === 'always' || (rule === 'auth' && authed) || (rule === 'admin' && admin);
    };

    var aside = document.createElement('aside');
    aside.className = 'jsb';
    var html = '<a class="jbrand" href="/"><span class="dot">🛢️</span> JASQL</a><nav>';
    GROUPS.forEach(function (g) {
      var items = g.items.filter(function (it) { return show(it.show); });
      if (!items.length) return;
      html += '<div class="jgrp">' + esc(g.title) + '</div>';
      items.forEach(function (it) {
        html += '<a class="jlink' + (it.key === cur ? ' active' : '') + '" href="' + it.href + '"' +
          (it.target ? ' target="' + it.target + '"' : '') + '>' +
          '<span class="ic">' + it.icon + '</span><span>' + esc(it.label) + '</span></a>';
      });
    });
    html += '</nav>';
    var ver = (me && me.version) ? ('v' + me.version) : '';
    html += '<div class="jfoot">JASQL ' + esc(ver) + (authed ? '' : ' · 단독 모드') + '</div>';
    aside.innerHTML = html;
    document.body.appendChild(aside);

    // mobile hamburger + scrim
    var toggle = document.createElement('button');
    toggle.className = 'jsb-toggle'; toggle.setAttribute('aria-label', '메뉴'); toggle.textContent = '☰';
    toggle.onclick = function () { document.body.classList.toggle('jsb-open'); };
    document.body.appendChild(toggle);
    var scrim = document.createElement('div');
    scrim.className = 'jsb-scrim';
    scrim.onclick = function () { document.body.classList.remove('jsb-open'); };
    document.body.appendChild(scrim);
  }

  function buildProfileMenu(me) {
    var header = document.querySelector('header');
    if (!header) return;
    if (!header.querySelector('.grow')) {
      var grow = document.createElement('span'); grow.className = 'grow'; grow.style.flex = '1';
      header.appendChild(grow);
    }
    var u = me.user;
    var initial = (u.display_name || u.username || '?').trim().charAt(0).toUpperCase();
    var wrap = document.createElement('div');
    wrap.className = 'jprofile';
    var isLocal = u.provider === 'local' || !u.provider;
    wrap.innerHTML =
      '<button class="javatar" type="button"><span class="jav">' + esc(initial) + '</span>' +
        '<span>' + esc(u.display_name || u.username) + '</span> <span style="opacity:.7">▾</span></button>' +
      '<div class="jmenu">' +
        '<div class="jmhead"><b>' + esc(u.display_name || u.username) + '</b>' +
          '<div class="jmsub">' + esc(u.email || u.username) + ' · ' + esc(u.role) + '</div></div>' +
        '<button class="jmitem" data-act="profile">👤 개인정보 변경</button>' +
        (isLocal ? '<button class="jmitem" data-act="password">🔒 비밀번호 변경</button>' : '') +
        '<button class="jmitem" data-act="keys">🔑 MCP 키 관리</button>' +
        '<div class="jmsep"></div>' +
        '<button class="jmitem danger" data-act="logout">⏻ 로그아웃</button>' +
        '<div class="jmfoot">JASQL v' + esc(me.version || '') + '</div>' +
      '</div>';
    header.appendChild(wrap);

    var btn = wrap.querySelector('.javatar');
    btn.onclick = function (e) { e.stopPropagation(); wrap.classList.toggle('open'); };
    document.addEventListener('click', function () { wrap.classList.remove('open'); });
    wrap.querySelector('.jmenu').addEventListener('click', function (e) { e.stopPropagation(); });
    wrap.querySelectorAll('.jmitem').forEach(function (item) {
      item.onclick = function () {
        wrap.classList.remove('open');
        var act = item.getAttribute('data-act');
        if (act === 'logout') { fetch('/auth/logout', { method: 'POST' }).then(function () { location.href = '/auth/login'; }); }
        else if (act === 'keys') { location.href = '/admin/keys'; }
        else if (act === 'profile') { openProfileModal(u); }
        else if (act === 'password') { openPasswordModal(); }
      };
    });
  }

  // ---- modals ----
  function modalShell(title, sub, bodyHTML, onSubmit) {
    var host = document.createElement('div');
    host.className = 'jmodal';
    host.innerHTML =
      '<div class="box"><h3>' + esc(title) + '</h3><p class="sub">' + esc(sub) + '</p>' +
      '<form>' + bodyHTML +
      '<div class="msg"></div>' +
      '<div class="acts"><button type="button" class="jb" data-x>취소</button>' +
      '<button type="submit" class="jb primary">저장</button></div></form></div>';
    document.body.appendChild(host);
    var close = function () { host.remove(); };
    host.addEventListener('click', function (e) { if (e.target === host) close(); });
    host.querySelector('[data-x]').onclick = close;
    var msg = host.querySelector('.msg');
    host.querySelector('form').onsubmit = function (e) {
      e.preventDefault();
      onSubmit(host, function (ok, text) {
        msg.className = 'msg ' + (ok ? 'ok' : 'bad'); msg.textContent = text || '';
        if (ok) setTimeout(close, 800);
      });
    };
    requestAnimationFrame(function () { host.classList.add('open'); });
    var f = host.querySelector('input'); if (f) f.focus();
    return host;
  }

  function openProfileModal(u) {
    modalShell('개인정보 변경', '표시 이름과 이메일을 수정합니다. 역할/비밀번호는 여기서 바뀌지 않습니다.',
      '<label>표시 이름</label><input name="display_name" value="' + esc(u.display_name || '') + '" placeholder="' + esc(u.username) + '">' +
      '<label>이메일</label><input name="email" type="email" value="' + esc(u.email || '') + '" placeholder="you@example.com">',
      function (host, done) {
        var body = JSON.stringify({
          display_name: host.querySelector('[name=display_name]').value.trim(),
          email: host.querySelector('[name=email]').value.trim(),
        });
        fetch('/auth/profile', { method: 'PUT', headers: tokenHeaders(true), body: body })
          .then(function (r) { return r.json().then(function (d) { return { ok: r.ok, d: d }; }); })
          .then(function (res) {
            if (!res.ok) return done(false, res.d.error || '저장 실패');
            done(true, '저장되었습니다');
            setTimeout(function () { location.reload(); }, 700);
          }).catch(function (e) { done(false, e.message); });
      });
  }

  function openPasswordModal() {
    modalShell('비밀번호 변경', '현재 비밀번호 확인 후 새 비밀번호로 변경합니다.',
      '<label>현재 비밀번호</label><input name="old" type="password" autocomplete="current-password">' +
      '<label>새 비밀번호</label><input name="new" type="password" autocomplete="new-password">' +
      '<label>새 비밀번호 확인</label><input name="new2" type="password" autocomplete="new-password">',
      function (host, done) {
        var np = host.querySelector('[name=new]').value, np2 = host.querySelector('[name=new2]').value;
        if (np.length < 8) return done(false, '새 비밀번호는 8자 이상이어야 합니다');
        if (np !== np2) return done(false, '새 비밀번호가 일치하지 않습니다');
        fetch('/auth/password', { method: 'PUT', headers: tokenHeaders(true),
          body: JSON.stringify({ old_password: host.querySelector('[name=old]').value, new_password: np }) })
          .then(function (r) { return r.json().then(function (d) { return { ok: r.ok, d: d }; }); })
          .then(function (res) { done(res.ok, res.ok ? '변경되었습니다' : (res.d.error || '변경 실패')); })
          .catch(function (e) { done(false, e.message); });
      });
  }

  function handleTokenBox(authed) {
    var tok = document.getElementById('adminToken');
    if (!tok) return;
    tok.style.display = 'none';
    if (authed) return; // session replaces the token in auth mode
    var header = document.querySelector('header');
    if (!header) return;
    if (!header.querySelector('.grow')) {
      var grow = document.createElement('span'); grow.className = 'grow'; grow.style.flex = '1';
      header.appendChild(grow);
    }
    var gear = document.createElement('button');
    gear.type = 'button'; gear.title = '관리 토큰 입력 (필요 시)'; gear.textContent = '🔧';
    gear.style.cssText = 'background:#1d2b47;border:1px solid #35507f;color:#cfe0ff;border-radius:8px;padding:6px 11px;cursor:pointer;font-size:14px;';
    gear.onclick = function () { tok.style.display = tok.style.display === 'none' ? '' : 'none'; if (tok.style.display === '') tok.focus(); };
    header.appendChild(gear);
  }

  window.JASQL = {
    page: null,
    async mount(opts) {
      opts = opts || {};
      this.page = opts.page || null;
      injectStyles();
      var me = { auth_enabled: false };
      try { me = await (await fetch('/auth/me')).json(); } catch (e) { /* standalone fallback */ }
      if (me.auth_enabled && !me.authenticated) {
        location.href = '/auth/login?next=' + encodeURIComponent(location.pathname);
        return;
      }
      window.AUTH = me.auth_enabled && me.authenticated ? me : null;
      buildSidebar(me);
      if (me.auth_enabled && me.authenticated) buildProfileMenu(me);
      handleTokenBox(!!me.auth_enabled);
      if (typeof opts.onReady === 'function') {
        try { opts.onReady(me); } catch (e) { console.error('onReady', e); }
      }
    },
  };
})();
