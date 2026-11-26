// Static help guides for every platform+method that requires manual credentials.
// Each guide has: title, getKeyURL (primary action link), steps (array of strings or {text, url}).
// title and the step texts are locale keys; the dialog translates them when it renders.

export const HELP_GUIDES = {

  // ── Social ────────────────────────────────────────────────────────────────

  telegram: {
    apikey: {
      title: 'ui6ConnHelp.telegram_apikey_title',
      getKeyURL: 'https://t.me/BotFather',
      steps: [
        'ui6ConnHelp.telegram_apikey_1',
        'ui6ConnHelp.telegram_apikey_2',
        'ui6ConnHelp.telegram_apikey_3',
        'ui6ConnHelp.telegram_apikey_4',
        'ui6ConnHelp.telegram_apikey_5',
      ],
    },
  },

  // ── Services ──────────────────────────────────────────────────────────────

  github: {
    oauth: {
      title: 'ui6ConnHelp.github_oauth_title',
      getKeyURL: 'https://github.com/settings/developers',
      steps: [
        { text: 'ui6ConnHelp.github_oauth_1', url: 'https://github.com/settings/developers' },
        'ui6ConnHelp.github_oauth_2',
        'ui6ConnHelp.github_oauth_3',
        'ui6ConnHelp.github_oauth_4',
        'ui6ConnHelp.github_oauth_5',
        'ui6ConnHelp.github_oauth_6',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.github_apikey_title',
      getKeyURL: 'https://github.com/settings/tokens/new',
      steps: [
        { text: 'ui6ConnHelp.github_apikey_1', url: 'https://github.com/settings/profile' },
        'ui6ConnHelp.github_apikey_2',
        'ui6ConnHelp.github_apikey_3',
        'ui6ConnHelp.github_apikey_4',
        'ui6ConnHelp.github_apikey_5',
      ],
    },
  },

  notion: {
    oauth: {
      title: 'ui6ConnHelp.notion_oauth_title',
      getKeyURL: 'https://www.notion.so/my-integrations',
      steps: [
        { text: 'ui6ConnHelp.notion_oauth_1', url: 'https://www.notion.so/my-integrations' },
        'ui6ConnHelp.notion_oauth_2',
        'ui6ConnHelp.notion_oauth_3',
        'ui6ConnHelp.notion_oauth_4',
        'ui6ConnHelp.notion_oauth_5',
        'ui6ConnHelp.notion_oauth_6',
        'ui6ConnHelp.notion_oauth_7',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.notion_apikey_title',
      getKeyURL: 'https://www.notion.so/my-integrations',
      steps: [
        { text: 'ui6ConnHelp.notion_apikey_1', url: 'https://www.notion.so/my-integrations' },
        'ui6ConnHelp.notion_apikey_2',
        'ui6ConnHelp.notion_apikey_3',
        'ui6ConnHelp.notion_apikey_4',
        'ui6ConnHelp.notion_apikey_5',
        'ui6ConnHelp.notion_apikey_6',
      ],
    },
  },

  airtable: {
    oauth: {
      title: 'ui6ConnHelp.airtable_oauth_title',
      getKeyURL: 'https://airtable.com/create/oauth',
      steps: [
        { text: 'ui6ConnHelp.airtable_oauth_1', url: 'https://airtable.com/create/oauth' },
        'ui6ConnHelp.airtable_oauth_2',
        'ui6ConnHelp.airtable_oauth_3',
        'ui6ConnHelp.airtable_oauth_4',
        'ui6ConnHelp.airtable_oauth_5',
        'ui6ConnHelp.airtable_oauth_6',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.airtable_apikey_title',
      getKeyURL: 'https://airtable.com/create/tokens',
      steps: [
        { text: 'ui6ConnHelp.airtable_apikey_1', url: 'https://airtable.com/create/tokens' },
        'ui6ConnHelp.airtable_apikey_2',
        'ui6ConnHelp.airtable_apikey_3',
        'ui6ConnHelp.airtable_apikey_4',
        'ui6ConnHelp.airtable_apikey_5',
      ],
    },
  },

  jira: {
    oauth: {
      title: 'ui6ConnHelp.jira_oauth_title',
      getKeyURL: 'https://developer.atlassian.com/console/myapps/',
      steps: [
        { text: 'ui6ConnHelp.jira_oauth_1', url: 'https://developer.atlassian.com/console/myapps/' },
        'ui6ConnHelp.jira_oauth_2',
        'ui6ConnHelp.jira_oauth_3',
        'ui6ConnHelp.jira_oauth_4',
        'ui6ConnHelp.jira_oauth_5',
        'ui6ConnHelp.jira_oauth_6',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.jira_apikey_title',
      getKeyURL: 'https://id.atlassian.com/manage-profile/security/api-tokens',
      steps: [
        { text: 'ui6ConnHelp.jira_apikey_1', url: 'https://id.atlassian.com/manage-profile/security/api-tokens' },
        'ui6ConnHelp.jira_apikey_2',
        'ui6ConnHelp.jira_apikey_3',
        'ui6ConnHelp.jira_apikey_4',
        'ui6ConnHelp.jira_apikey_5',
      ],
    },
  },

  linear: {
    oauth: {
      title: 'ui6ConnHelp.linear_oauth_title',
      getKeyURL: 'https://linear.app/settings/api/applications/new',
      steps: [
        { text: 'ui6ConnHelp.linear_oauth_1', url: 'https://linear.app/settings/api/applications/new' },
        'ui6ConnHelp.linear_oauth_2',
        'ui6ConnHelp.linear_oauth_3',
        'ui6ConnHelp.linear_oauth_4',
        'ui6ConnHelp.linear_oauth_5',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.linear_apikey_title',
      getKeyURL: 'https://linear.app/settings/api',
      steps: [
        { text: 'ui6ConnHelp.linear_apikey_1', url: 'https://linear.app/settings/api' },
        'ui6ConnHelp.linear_apikey_2',
        'ui6ConnHelp.linear_apikey_3',
        'ui6ConnHelp.linear_apikey_4',
      ],
    },
  },

  asana: {
    oauth: {
      title: 'ui6ConnHelp.asana_oauth_title',
      getKeyURL: 'https://app.asana.com/0/my-apps',
      steps: [
        { text: 'ui6ConnHelp.asana_oauth_1', url: 'https://app.asana.com/0/my-apps' },
        'ui6ConnHelp.asana_oauth_2',
        'ui6ConnHelp.asana_oauth_3',
        'ui6ConnHelp.asana_oauth_4',
        'ui6ConnHelp.asana_oauth_5',
        'ui6ConnHelp.asana_oauth_6',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.asana_apikey_title',
      getKeyURL: 'https://app.asana.com/0/my-apps',
      steps: [
        { text: 'ui6ConnHelp.asana_apikey_1', url: 'https://app.asana.com/0/my-apps' },
        'ui6ConnHelp.asana_apikey_2',
        'ui6ConnHelp.asana_apikey_3',
        'ui6ConnHelp.asana_apikey_4',
      ],
    },
  },

  stripe: {
    apikey: {
      title: 'ui6ConnHelp.stripe_apikey_title',
      getKeyURL: 'https://dashboard.stripe.com/apikeys',
      steps: [
        { text: 'ui6ConnHelp.stripe_apikey_1', url: 'https://dashboard.stripe.com/apikeys' },
        'ui6ConnHelp.stripe_apikey_2',
        'ui6ConnHelp.stripe_apikey_3',
        'ui6ConnHelp.stripe_apikey_4',
      ],
    },
  },

  shopify: {
    apikey: {
      title: 'ui6ConnHelp.shopify_apikey_title',
      getKeyURL: 'https://help.shopify.com/en/manual/apps/app-types/custom-apps',
      steps: [
        'ui6ConnHelp.shopify_apikey_1',
        'ui6ConnHelp.shopify_apikey_2',
        'ui6ConnHelp.shopify_apikey_3',
        'ui6ConnHelp.shopify_apikey_4',
        'ui6ConnHelp.shopify_apikey_5',
        'ui6ConnHelp.shopify_apikey_6',
        'ui6ConnHelp.shopify_apikey_7',
      ],
    },
  },

  hubspot: {
    oauth: {
      title: 'ui6ConnHelp.hubspot_oauth_title',
      getKeyURL: 'https://developers.hubspot.com/get-started',
      steps: [
        { text: 'ui6ConnHelp.hubspot_oauth_1', url: 'https://developers.hubspot.com/get-started' },
        'ui6ConnHelp.hubspot_oauth_2',
        'ui6ConnHelp.hubspot_oauth_3',
        'ui6ConnHelp.hubspot_oauth_4',
        'ui6ConnHelp.hubspot_oauth_5',
        'ui6ConnHelp.hubspot_oauth_6',
        'ui6ConnHelp.hubspot_oauth_7',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apikey: {
      title: 'ui6ConnHelp.hubspot_apikey_title',
      getKeyURL: 'https://app.hubspot.com/private-apps',
      steps: [
        { text: 'ui6ConnHelp.hubspot_apikey_1', url: 'https://app.hubspot.com/private-apps' },
        'ui6ConnHelp.hubspot_apikey_2',
        'ui6ConnHelp.hubspot_apikey_3',
        'ui6ConnHelp.hubspot_apikey_4',
        'ui6ConnHelp.hubspot_apikey_5',
        'ui6ConnHelp.hubspot_apikey_6',
      ],
    },
  },

  gmail: {
    oauth: {
      title: 'ui6ConnHelp.gmail_oauth_title',
      getKeyURL: 'https://console.cloud.google.com/apis/credentials',
      steps: [
        { text: 'ui6ConnHelp.gmail_oauth_1', url: 'https://console.cloud.google.com/projectcreate' },
        { text: 'ui6ConnHelp.gmail_oauth_2', url: 'https://console.cloud.google.com/apis/library/gmail.googleapis.com' },
        { text: 'ui6ConnHelp.gmail_oauth_3', url: 'https://console.cloud.google.com/apis/credentials' },
        'ui6ConnHelp.gmail_oauth_4',
        'ui6ConnHelp.gmail_oauth_5',
        'ui6ConnHelp.gmail_oauth_6',
        'ui6ConnHelp.gmail_oauth_7',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
    apppassword: {
      title: 'ui6ConnHelp.gmail_apppassword_title',
      getKeyURL: 'https://myaccount.google.com/apppasswords',
      steps: [
        'ui6ConnHelp.gmail_apppassword_1',
        { text: 'ui6ConnHelp.gmail_apppassword_2', url: 'https://myaccount.google.com/apppasswords' },
        'ui6ConnHelp.gmail_apppassword_3',
        'ui6ConnHelp.gmail_apppassword_4',
        'ui6ConnHelp.gmail_apppassword_5',
        'ui6ConnHelp.gmail_apppassword_6',
      ],
    },
  },

  outlook: {
    oauth: {
      title: 'ui6ConnHelp.outlook_oauth_title',
      getKeyURL: 'https://portal.azure.com',
      steps: [
        { text: 'ui6ConnHelp.outlook_oauth_1', url: 'https://portal.azure.com' },
        'ui6ConnHelp.outlook_oauth_2',
        'ui6ConnHelp.outlook_oauth_3',
        'ui6ConnHelp.outlook_oauth_4',
        'ui6ConnHelp.outlook_oauth_5',
        'ui6ConnHelp.outlook_oauth_6',
        'ui6ConnHelp.outlook_oauth_7',
      ],
    },
    apppassword: {
      title: 'ui6ConnHelp.outlook_apppassword_title',
      getKeyURL: 'https://account.microsoft.com/security',
      steps: [
        'ui6ConnHelp.outlook_apppassword_1',
        'ui6ConnHelp.outlook_apppassword_2',
        { text: 'ui6ConnHelp.outlook_apppassword_3', url: 'https://account.microsoft.com/security' },
        'ui6ConnHelp.outlook_apppassword_4',
        'ui6ConnHelp.outlook_apppassword_5',
        'ui6ConnHelp.outlook_apppassword_6',
      ],
    },
  },

  slack: {
    oauth: {
      title: 'ui6ConnHelp.slack_oauth_title',
      getKeyURL: 'https://api.slack.com/apps/new',
      steps: [
        { text: 'ui6ConnHelp.slack_oauth_1', url: 'https://api.slack.com/apps/new' },
        'ui6ConnHelp.slack_oauth_2',
        'ui6ConnHelp.slack_oauth_3',
        'ui6ConnHelp.slack_oauth_4',
        'ui6ConnHelp.slack_oauth_5',
        'ui6ConnHelp.slack_oauth_6',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
  },

  google_sheets: {
    oauth: {
      title: 'ui6ConnHelp.google_sheets_oauth_title',
      getKeyURL: 'https://console.cloud.google.com/apis/credentials',
      steps: [
        { text: 'ui6ConnHelp.google_sheets_oauth_1', url: 'https://console.cloud.google.com/projectcreate' },
        { text: 'ui6ConnHelp.google_sheets_oauth_2', url: 'https://console.cloud.google.com/apis/library/sheets.googleapis.com' },
        { text: 'ui6ConnHelp.google_sheets_oauth_3', url: 'https://console.cloud.google.com/apis/credentials' },
        'ui6ConnHelp.google_sheets_oauth_4',
        'ui6ConnHelp.google_sheets_oauth_5',
        'ui6ConnHelp.google_sheets_oauth_6',
        'ui6ConnHelp.google_sheets_oauth_7',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
  },

  google_drive: {
    oauth: {
      title: 'ui6ConnHelp.google_drive_oauth_title',
      getKeyURL: 'https://console.cloud.google.com/apis/credentials',
      steps: [
        { text: 'ui6ConnHelp.google_drive_oauth_1', url: 'https://console.cloud.google.com/projectcreate' },
        { text: 'ui6ConnHelp.google_drive_oauth_2', url: 'https://console.cloud.google.com/apis/library/drive.googleapis.com' },
        { text: 'ui6ConnHelp.google_drive_oauth_3', url: 'https://console.cloud.google.com/apis/credentials' },
        'ui6ConnHelp.google_drive_oauth_4',
        'ui6ConnHelp.google_drive_oauth_5',
        'ui6ConnHelp.google_drive_oauth_6',
        'ui6ConnHelp.google_drive_oauth_7',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
  },

  salesforce: {
    oauth: {
      title: 'ui6ConnHelp.salesforce_oauth_title',
      getKeyURL: 'https://login.salesforce.com',
      steps: [
        'ui6ConnHelp.salesforce_oauth_1',
        'ui6ConnHelp.salesforce_oauth_2',
        'ui6ConnHelp.salesforce_oauth_3',
        'ui6ConnHelp.salesforce_oauth_4',
        'ui6ConnHelp.salesforce_oauth_5',
        'ui6ConnHelp.salesforce_oauth_6',
        'ui6ConnHelp.salesforce_oauth_7',
        'ui6ConnHelp.salesforce_oauth_8',
        'ui6ConnHelp.salesforce_oauth_9',
        'ui6ConnHelp.paste_client_fields',
      ],
    },
  },

  // ── Communication ─────────────────────────────────────────────────────────

  discord: {
    apikey: {
      title: 'ui6ConnHelp.discord_apikey_title',
      getKeyURL: 'https://discord.com/developers/applications',
      steps: [
        { text: 'ui6ConnHelp.discord_apikey_1', url: 'https://discord.com/developers/applications' },
        'ui6ConnHelp.discord_apikey_2',
        'ui6ConnHelp.discord_apikey_3',
        'ui6ConnHelp.discord_apikey_4',
        'ui6ConnHelp.discord_apikey_5',
        'ui6ConnHelp.discord_apikey_6',
      ],
    },
  },

  twilio: {
    apikey: {
      title: 'ui6ConnHelp.twilio_apikey_title',
      getKeyURL: 'https://console.twilio.com/',
      steps: [
        { text: 'ui6ConnHelp.twilio_apikey_1', url: 'https://console.twilio.com/' },
        'ui6ConnHelp.twilio_apikey_2',
        'ui6ConnHelp.twilio_apikey_3',
        'ui6ConnHelp.twilio_apikey_4',
        'ui6ConnHelp.twilio_apikey_5',
        'ui6ConnHelp.twilio_apikey_6',
      ],
    },
  },

  whatsapp: {
    apikey: {
      title: 'ui6ConnHelp.whatsapp_apikey_title',
      getKeyURL: 'https://developers.facebook.com/',
      steps: [
        { text: 'ui6ConnHelp.whatsapp_apikey_1', url: 'https://developers.facebook.com/' },
        'ui6ConnHelp.whatsapp_apikey_2',
        'ui6ConnHelp.whatsapp_apikey_3',
        'ui6ConnHelp.whatsapp_apikey_4',
        'ui6ConnHelp.whatsapp_apikey_5',
        'ui6ConnHelp.whatsapp_apikey_6',
      ],
    },
  },

  smtp: {
    apppassword: {
      title: 'ui6ConnHelp.smtp_apppassword_title',
      getKeyURL: 'https://support.google.com/mail/answer/7126229',
      steps: [
        'ui6ConnHelp.smtp_apppassword_1',
        'ui6ConnHelp.smtp_apppassword_2',
        'ui6ConnHelp.smtp_apppassword_3',
        'ui6ConnHelp.smtp_apppassword_4',
        'ui6ConnHelp.smtp_apppassword_5',
        'ui6ConnHelp.smtp_apppassword_6',
      ],
    },
  },

  // ── Databases ─────────────────────────────────────────────────────────────

  postgresql: {
    connstring: {
      title: 'ui6ConnHelp.postgresql_connstring_title',
      getKeyURL: 'https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING',
      steps: [
        'ui6ConnHelp.postgresql_connstring_1',
        'ui6ConnHelp.postgresql_connstring_2',
        'ui6ConnHelp.postgresql_connstring_3',
        'ui6ConnHelp.postgresql_connstring_4',
        'ui6ConnHelp.postgresql_connstring_5',
        'ui6ConnHelp.postgresql_connstring_6',
      ],
    },
  },

  mysql: {
    connstring: {
      title: 'ui6ConnHelp.mysql_connstring_title',
      getKeyURL: 'https://pkg.go.dev/github.com/go-mysql-org/go-mysql/driver',
      steps: [
        'ui6ConnHelp.mysql_connstring_1',
        'ui6ConnHelp.mysql_connstring_2',
        'ui6ConnHelp.mysql_connstring_3',
        'ui6ConnHelp.mysql_connstring_4',
        'ui6ConnHelp.mysql_connstring_5',
        'ui6ConnHelp.mysql_connstring_6',
      ],
    },
  },

  mongodb: {
    connstring: {
      title: 'ui6ConnHelp.mongodb_connstring_title',
      getKeyURL: 'https://www.mongodb.com/docs/manual/reference/connection-string/',
      steps: [
        'ui6ConnHelp.mongodb_connstring_1',
        'ui6ConnHelp.mongodb_connstring_2',
        'ui6ConnHelp.mongodb_connstring_3',
        'ui6ConnHelp.mongodb_connstring_4',
        'ui6ConnHelp.mongodb_connstring_5',
      ],
    },
  },

  redis: {
    connstring: {
      title: 'ui6ConnHelp.redis_connstring_title',
      getKeyURL: 'https://redis.io/docs/manual/cli/',
      steps: [
        'ui6ConnHelp.redis_connstring_1',
        'ui6ConnHelp.redis_connstring_2',
        'ui6ConnHelp.redis_connstring_3',
        'ui6ConnHelp.redis_connstring_4',
        'ui6ConnHelp.redis_connstring_5',
        'ui6ConnHelp.redis_connstring_6',
      ],
    },
  },
}
