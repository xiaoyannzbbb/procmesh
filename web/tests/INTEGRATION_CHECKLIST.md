# i18n Integration Test Checklist

## Manual Testing

### Language Switching
- [ ] English is default on first visit
- [ ] Language switcher is visible on all pages
- [ ] Clicking language button switches immediately
- [ ] Language choice persists after page reload
- [ ] Language choice persists after browser restart

### UI Translation
- [ ] All navigation links translate
- [ ] All button labels translate
- [ ] All form labels translate
- [ ] All table headers translate
- [ ] All status indicators translate

### Navigation
- [ ] Overview page displays correctly in English
- [ ] Overview page displays correctly in Chinese
- [ ] Nodes page displays correctly in English
- [ ] Nodes page displays correctly in Chinese
- [ ] Processes page displays correctly in English
- [ ] Processes page displays correctly in Chinese

### Error Messages
- [ ] Login errors display in selected language
- [ ] API errors display with correct interpolation
- [ ] Unknown error codes fall back to English

### Audit Logs (if available)
- [ ] Action descriptions translate with parameters
- [ ] Result codes translate
- [ ] Timestamps remain in ISO format

### Performance
- [ ] Initial page load is fast (<2s)
- [ ] Language switching is instant (<100ms)
- [ ] Namespace loading is transparent
- [ ] No visible flickering during translation

## Browser Compatibility
- [ ] Chrome/Chromium
- [ ] Firefox
- [ ] Safari
- [ ] Edge

## Accessibility
- [ ] Screen reader announces language changes
- [ ] Focus management works after language switch
- [ ] Keyboard navigation works in both languages
