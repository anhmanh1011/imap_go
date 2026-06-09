// PM2 config for the imap_checker batch run.
// autorestart:false — this is a finite job over input.txt, not a daemon;
// PM2 must NOT relaunch it (and re-process 8.7M creds) when it exits cleanly.
module.exports = {
  apps: [
    {
      name: 'imap-check-w5000',
      cwd: '/root/imap_checker',
      script: './imap_checker',
      args: [
        '-input', 'input.txt',
        '-proxies', 'proxies/rotating.txt',
        '-workers', '20000',
        '-db', './Servers.db',
        '-out', 'output/run-w5000-budgetpool-20260603',
      ],
      autorestart: false,
      max_restarts: 0,
      kill_timeout: 15000, // give the writer time to flush + close on stop
      out_file: 'logs/pm2-imap-w5000.out.log',
      error_file: 'logs/pm2-imap-w5000.err.log',
      merge_logs: true,
      time: true,
    },
    {
      name: 'tgbot',
      cwd: '/root/imap_checker',
      script: './tgbot',
      args: ['tgbot.env'],
      autorestart: true,
      max_restarts: 10,
      kill_timeout: 20000,
      out_file: 'logs/pm2-tgbot.out.log',
      error_file: 'logs/pm2-tgbot.err.log',
      merge_logs: true,
      time: true,
    },
  ],
}
