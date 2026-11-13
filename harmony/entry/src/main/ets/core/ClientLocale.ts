let preference: string = 'default';
let initialized: boolean = false;

/** Ability recreation in the same process is not a cold application launch. */
export function initializeClientLanguage(language: string): void {
  if (initialized) return;
  setClientLanguage(language); initialized = true;
}

export function setClientLanguage(language: string): void {
  preference = ['en-US', 'zh-CN', 'ja-JP'].includes(language) ? language : 'default';
}

/** Wire locale follows the committed UI preference; only system mode reads the OS language. */
export function clientLocale(systemLanguage: string): string {
  const language = (preference === 'default' ? systemLanguage : preference).toLowerCase().split(/[-_]/)[0];
  return language === 'zh' ? 'zh-Hans' : language === 'ja' ? 'ja' : 'en';
}
