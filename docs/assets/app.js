// JAMYPG Interactive App JS

document.addEventListener('DOMContentLoaded', () => {
  initLanguageSwitcher();
  initSimulatorTabs();
  initToolFilter();
  initCodeTabs();
  initFAQAccordion();
});

/* Language Switcher (Korean Default + English Support) */
function initLanguageSwitcher() {
  const currentLang = localStorage.getItem('jamypg_lang') || 'ko';
  setLanguage(currentLang);

  document.querySelectorAll('.lang-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const lang = btn.getAttribute('data-lang');
      setLanguage(lang);
    });
  });
}

function setLanguage(lang) {
  localStorage.setItem('jamypg_lang', lang);
  document.documentElement.lang = lang;

  // Update active state on switcher buttons
  document.querySelectorAll('.lang-btn').forEach(btn => {
    if (btn.getAttribute('data-lang') === lang) {
      btn.classList.add('active');
    } else {
      btn.classList.remove('active');
    }
  });

  // Switch all elements with data-ko and data-en
  document.querySelectorAll('[data-ko]').forEach(el => {
    const koText = el.getAttribute('data-ko');
    const enText = el.getAttribute('data-en');
    
    if (lang === 'en' && enText) {
      el.textContent = enText;
    } else if (lang === 'ko' && koText) {
      el.textContent = koText;
    }
  });

  // HTML content switching for structured markup
  document.querySelectorAll('[data-html-ko]').forEach(el => {
    const koHtml = el.getAttribute('data-html-ko');
    const enHtml = el.getAttribute('data-html-en');
    
    if (lang === 'en' && enHtml) {
      el.innerHTML = enHtml;
    } else if (lang === 'ko' && koHtml) {
      el.innerHTML = koHtml;
    }
  });
}

/* Simulator Tabs (Workflow Simulation) */
function initSimulatorTabs() {
  const tabs = document.querySelectorAll('.sim-tab');
  const steps = document.querySelectorAll('.sim-step');

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const targetStep = tab.getAttribute('data-step');
      
      tabs.forEach(t => t.classList.remove('active'));
      steps.forEach(s => s.classList.remove('active'));

      tab.classList.add('active');
      const activeStep = document.getElementById(`sim-${targetStep}`);
      if (activeStep) activeStep.classList.add('active');
    });
  });
}

/* Tool Showcase Filter */
function initToolFilter() {
  const tabs = document.querySelectorAll('.tool-tab');
  const items = document.querySelectorAll('.tool-item');

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const category = tab.getAttribute('data-category');

      tabs.forEach(t => t.classList.remove('active'));
      tab.classList.add('active');

      items.forEach(item => {
        if (category === 'all' || item.getAttribute('data-category') === category) {
          item.style.display = 'flex';
        } else {
          item.style.display = 'none';
        }
      });
    });
  });
}

/* Code Snippet Tabs & Copy */
function initCodeTabs() {
  const navBtns = document.querySelectorAll('.code-nav-btn');

  navBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.getAttribute('data-target');
      
      navBtns.forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.code-content').forEach(c => c.style.display = 'none');

      btn.classList.add('active');
      const targetCode = document.getElementById(targetId);
      if (targetCode) targetCode.style.display = 'block';
    });
  });

  // Copy buttons
  document.querySelectorAll('.copy-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const wrapper = btn.closest('.code-block-wrapper');
      const code = wrapper.querySelector('pre').innerText;
      
      navigator.clipboard.writeText(code).then(() => {
        showToast(document.documentElement.lang === 'ko' ? '복사되었습니다!' : 'Copied to clipboard!');
      });
    });
  });
}

/* FAQ Accordion */
function initFAQAccordion() {
  const faqItems = document.querySelectorAll('.faq-item');

  faqItems.forEach(item => {
    const question = item.querySelector('.faq-question');
    question.addEventListener('click', () => {
      const isOpen = item.classList.contains('open');
      
      // Close all other items
      faqItems.forEach(i => i.classList.remove('open'));

      if (!isOpen) {
        item.classList.add('open');
      }
    });
  });
}

/* Toast Notification */
function showToast(msg) {
  let toast = document.getElementById('toast-notification');
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'toast-notification';
    toast.className = 'toast';
    document.body.appendChild(toast);
  }

  toast.innerHTML = `<span>✓</span> <span>${msg}</span>`;
  toast.classList.add('show');

  setTimeout(() => {
    toast.classList.remove('show');
  }, 2500);
}

  // Mobile Menu Toggle
  const mobileBtn = document.getElementById('mobile-menu-btn');
  const navLinks = document.querySelector('.nav-links') || document.querySelector('.nav-menu');
  if (mobileBtn && navLinks) {
    mobileBtn.addEventListener('click', () => {
      navLinks.classList.toggle('active');
    });
    navLinks.querySelectorAll('a').forEach(link => {
      link.addEventListener('click', () => navLinks.classList.remove('active'));
    });
  }
