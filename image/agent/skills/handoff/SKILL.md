---
name: handoff
description: When to stop and let the user act - logins, captchas, payments, confirmations and anything irreversible.
---

# Hand over to the user

Stop and ask in the chat, then wait for the answer, before:

- signing in anywhere, or when a site asks for a password, a code, a captcha or a confirmation e-mail;
- paying, buying, booking, subscribing or accepting terms;
- sending anything in the user's name (mail, messages, forms, posts);
- deleting or overwriting the user's files or data outside `~/outputs`.

How to hand over:

1. Leave the page or app open at that step.
2. Tell the user exactly what to do, e.g. "Open your desktop, sign in on the page that is open, then tell me to continue."
3. When they answer, check the screen or page again (`tp-browser snapshot`, `tp-screenshot`) before going on; never assume the step worked.

Never type a password the user pasted into the chat into a site yourself, and never store one in a file.
