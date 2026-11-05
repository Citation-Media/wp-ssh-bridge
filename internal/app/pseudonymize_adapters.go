package app

import "sort"

// Strategies the filter understands. Values that are empty or NULL stay as they
// are, so absent data does not turn into invented data.
const (
	strategyEmail     = "email"
	strategyFirstName = "first_name"
	strategyLastName  = "last_name"
	strategyFullName  = "full_name"
	strategyLogin     = "login"
	strategyStreet    = "street"
	strategyCity      = "city"
	strategyPostcode  = "postcode"
	strategyCompany   = "company"
	strategyPhone     = "phone"
	strategyIP        = "ip"
	strategyUserAgent = "user_agent"
	strategyText      = "text"
	strategyToken     = "token"
	strategyPassword  = "password"
	strategyLogins    = "logins"
	strategyBlank     = "blank"
)

// customRuleStrategies are the ones a project may name in pseudonymize_rules.
var customRuleStrategies = map[string]bool{
	strategyEmail: true, strategyFirstName: true, strategyLastName: true, strategyFullName: true,
	strategyLogin: true, strategyStreet: true, strategyCity: true, strategyPostcode: true,
	strategyCompany: true, strategyPhone: true, strategyIP: true, strategyUserAgent: true,
	strategyText: true, strategyToken: true, strategyBlank: true,
}

// privatePostsMap holds the IDs of posts that are records about a person rather than
// site content. Sensitive meta keys are only blanked for those, so a store locator's
// public address survives while an order's does not.
const privatePostsMap = "private_posts"

// sensitiveMetaKeys matches meta keys that plugins commonly use for personal data,
// credentials, and payment references. Keys are free-form, so this cannot be exhaustive;
// the email scan and project rules cover what it misses.
const sensitiveMetaKeys = `(?i)(?:e-?mail|phone|(?:^|_)mobile|first_?name|last_?name|full_?name|address|postcode|postal|(?:^|_)zip|(?:^|_)city(?:_|$)|(?:^|_)ip(?:_address)?(?:_|$)|user_?agent|iban|(?:^|_)bic(?:_|$)|(?:^|_)vat(?:_|$)|ustid|tax_?id|birth|(?:^|_)dob(?:_|$)|company|transaction|stripe|ppcp|paypal|klarna|mollie|payer|token|secret|password|api_?key|payment_meta|user_info)`

// credentialKeys matches settings that hold a secret, anchored at the end so that an option
// such as woocommerce_myaccount_lost_password_endpoint keeps its value.
const credentialKeys = `(?i)(?:secret(?:_?key)?|passw(?:or)?d|(?:^|_)pass|api_?key|(?:access|refresh|auth|bearer)_?token|private_?key|license_?key|consumer_?key)(?:_?(?:live|test|sandbox|prod|production))?$`

// Country and state decide tax and shipping, and are too coarse to identify anyone.
const coarseLocationMetaKeys = `^_?(?:billing|shipping)_(?:country|state)$`

// pseudonymizeAdapter declares what one product stores about people. Adapters only
// describe tables; the filter owns the mechanics, so supporting another plugin means
// adding data here and a scenario in e2e/lib/scenarios.sh.
type pseudonymizeAdapter struct {
	Tables []tableRule
	// DumpFirst lists tables other tables look people up in, in the order they must be
	// exported. The filter streams, so a lookup has to precede its use. It also lists the
	// tables holding people's names, so the free text after them can be checked for those.
	DumpFirst []string
	// Protect lists, per table, the columns a site needs as they are: an identifier that
	// code compares, or a value a pattern would only mistake for personal data. They are
	// spared everything but the email scan. Rules still win where a column has one.
	Protect map[string][]string
}

// tableRule is one table's treatment. Table names are relative to the installation
// prefix. A table that is not Global also matches each multisite subsite's copy.
type tableRule struct {
	Name          string
	Global        bool
	Drop          bool
	RegisterUsers bool
	Columns       map[string]string
	Conditions    []ruleCondition
	Identity      *identityRule
	Collect       []collectRule
	Meta          *metaRule
	// NameSkip spots rows whose name column holds no person, such as a pingback's site.
	NameSkip *ruleCondition
	// Phrases are columns naming the site's own things. A person's name that equals one of
	// them, as a brand's often does, is left alone in free text.
	Phrases []string
}

// ruleCondition applies extra column strategies to the rows whose column has one of the
// listed values.
type ruleCondition struct {
	Column  string
	In      []string
	Columns map[string]string
}

// identityRule says how a row names its person, in order of preference: a user ID, an
// email address, then an ID another table already resolved. Without any of them, rows
// sharing RowColumn share one pseudonym (default: the table's first column).
type identityRule struct {
	UserColumn  string
	EmailColumn string
	LinkColumn  string
	LinkMap     string
	RowColumn   string
}

// collectRule records a row's ID while the table streams by, for tables exported later.
type collectRule struct {
	Map           string
	IDColumn      string
	Where         *ruleCondition
	StoreIdentity bool
}

// metaRule describes a key/value table, where the strategy depends on the key.
type metaRule struct {
	KeyColumn          string
	ValueColumn        string
	ObjectColumn       string
	Keys               map[string]string
	DropKeys           []string
	DropPatterns       []string
	KeepPatterns       []string
	ProtectKeys        []string
	ProtectPatterns    []string
	PhraseKeys         []string
	SensitivePattern   string
	SensitiveOnlyInMap string
}

func standardMeta(object string) *metaRule {
	return &metaRule{
		KeyColumn:        "meta_key",
		ValueColumn:      "meta_value",
		ObjectColumn:     object,
		KeepPatterns:     []string{coarseLocationMetaKeys},
		SensitivePattern: sensitiveMetaKeys,
	}
}

// personAddressKeys renders the billing and shipping fields WooCommerce stores in user
// meta and, with a leading underscore, in legacy order meta.
func personAddressKeys(prefix string, keys map[string]string) {
	for _, scope := range []string{"billing", "shipping"} {
		name := prefix + scope
		keys[name+"_first_name"] = strategyFirstName
		keys[name+"_last_name"] = strategyLastName
		keys[name+"_company"] = strategyCompany
		keys[name+"_address_1"] = strategyStreet
		keys[name+"_address_2"] = strategyBlank
		keys[name+"_city"] = strategyCity
		keys[name+"_postcode"] = strategyPostcode
		keys[name+"_phone"] = strategyPhone
		keys[name+"_email"] = strategyEmail
	}
}

// wordpressAdapter covers core, single site and multisite.
func wordpressAdapter() pseudonymizeAdapter {
	return pseudonymizeAdapter{
		DumpFirst: []string{"users", "usermeta", "terms"},
		Protect: map[string][]string{
			"posts":         {"post_type", "post_status", "post_name", "guid", "post_mime_type", "comment_status", "ping_status"},
			"comments":      {"comment_type", "comment_approved"},
			"terms":         {"name", "slug"},
			"term_taxonomy": {"taxonomy"},
		},
		Tables: []tableRule{
			{
				Name: "users", Global: true, RegisterUsers: true,
				Identity: &identityRule{UserColumn: "ID"},
				Columns: map[string]string{
					"user_login":          strategyLogin,
					"user_nicename":       strategyLogin,
					"user_email":          strategyEmail,
					"display_name":        strategyFullName,
					"user_pass":           strategyPassword,
					"user_url":            strategyBlank,
					"user_activation_key": strategyBlank,
				},
			},
			{
				Name: "usermeta", Global: true,
				Identity: &identityRule{UserColumn: "user_id", RowColumn: "user_id"},
				Meta: func() *metaRule {
					meta := standardMeta("user_id")
					meta.Keys = map[string]string{
						"first_name":  strategyFirstName,
						"last_name":   strategyLastName,
						"nickname":    strategyLogin,
						"description": strategyText,
					}
					// Session tokens and application passwords grant access, and the
					// events location stores the visitor's IP address.
					meta.DropKeys = []string{"session_tokens", "_application_passwords", "community-events-location"}
					return meta
				}(),
			},
			{
				Name:     "comments",
				Identity: &identityRule{UserColumn: "user_id", EmailColumn: "comment_author_email"},
				NameSkip: &ruleCondition{Column: "comment_type", In: []string{"pingback", "trackback"}},
				Columns: map[string]string{
					"comment_author":       strategyFullName,
					"comment_author_email": strategyEmail,
					"comment_author_url":   strategyBlank,
					"comment_author_IP":    strategyIP,
					"comment_agent":        strategyUserAgent,
					"comment_content":      strategyText,
				},
			},
			{
				Name: "commentmeta",
				Meta: func() *metaRule {
					meta := standardMeta("comment_id")
					// Akismet keeps a full copy of the submitted comment.
					meta.DropPatterns = []string{`^akismet_`}
					return meta
				}(),
			},
			{
				Name: "posts",
				Collect: []collectRule{{
					Map: privatePostsMap, IDColumn: "ID",
					Where: &ruleCondition{Column: "post_type", In: []string{"user_request"}},
				}},
				// Personal data export and erasure requests hold the address in the title.
				Conditions: []ruleCondition{{
					Column: "post_type", In: []string{"user_request"},
					Columns: map[string]string{
						"post_title": strategyEmail, "post_name": strategyToken,
						"post_content": strategyBlank, "post_excerpt": strategyBlank,
					},
				}},
				Identity: &identityRule{RowColumn: "ID"},
			},
			{Name: "terms", Phrases: []string{"name"}},
			{
				Name: "postmeta",
				Meta: func() *metaRule {
					meta := standardMeta("post_id")
					meta.SensitiveOnlyInMap = privatePostsMap
					meta.ProtectKeys = []string{"_wp_attached_file", "_wp_attachment_metadata", "_wp_page_template", "_thumbnail_id", "_edit_lock", "_edit_last", "_wp_old_slug", "_wp_old_date", "_wp_trash_meta_status", "_wp_trash_meta_time"}
					meta.ProtectPatterns = []string{`^_menu_item_`}
					return meta
				}(),
				Identity: &identityRule{RowColumn: "post_id"},
			},
			{
				Name: "options",
				Meta: &metaRule{
					KeyColumn: "option_name", ValueColumn: "option_value",
					Keys: map[string]string{"admin_email": strategyEmail, "new_admin_email": strategyEmail},
					// Transients cache whatever a plugin fetched, and WordPress rebuilds them.
					DropPatterns:     []string{`^_(?:site_)?transient_`},
					SensitivePattern: credentialKeys,
					ProtectKeys:      []string{"siteurl", "home", "blogname", "blogdescription", "template", "stylesheet", "active_plugins", "permalink_structure", "category_base", "tag_base", "rewrite_rules", "sidebars_widgets", "db_version", "initial_db_version", "WPLANG", "timezone_string", "date_format", "time_format", "upload_path", "upload_url_path"},
					ProtectPatterns:  []string{`_user_roles$`},
					PhraseKeys:       []string{"blogname"},
				},
			},
			{
				Name: "sitemeta", Global: true,
				Meta: &metaRule{
					KeyColumn: "meta_key", ValueColumn: "meta_value",
					Keys:             map[string]string{"admin_email": strategyEmail, "new_admin_email": strategyEmail, "site_admins": strategyLogins},
					DropPatterns:     []string{`^_site_transient_`},
					SensitivePattern: credentialKeys,
					ProtectKeys:      []string{"site_name", "siteurl", "active_sitewide_plugins"},
					PhraseKeys:       []string{"site_name"},
				},
			},
			{
				Name: "signups", Global: true,
				Identity: &identityRule{EmailColumn: "user_email"},
				Columns: map[string]string{
					"user_login": strategyLogin, "user_email": strategyEmail,
					"activation_key": strategyToken, "meta": strategyBlank,
				},
			},
			{
				Name: "registration_log", Global: true,
				Identity: &identityRule{EmailColumn: "email"},
				Columns:  map[string]string{"email": strategyEmail, "IP": strategyIP},
			},
		},
	}
}

// woocommerceAdapter covers both order stores: a shop on HPOS keeps orders in wc_orders,
// a legacy shop in posts and postmeta, and a shop syncing the two in both.
func woocommerceAdapter() pseudonymizeAdapter {
	orderTypes := []string{"shop_order", "shop_order_refund", "shop_order_placehold", "shop_subscription"}

	userKeys := map[string]string{}
	personAddressKeys("", userKeys)

	legacyOrderKeys := map[string]string{
		"_customer_ip_address":    strategyIP,
		"_customer_user_agent":    strategyUserAgent,
		"_billing_address_index":  strategyBlank,
		"_shipping_address_index": strategyBlank,
		"_transaction_id":         strategyToken,
		"_payment_tokens":         strategyBlank,
		"_customer_note":          strategyBlank,
	}
	personAddressKeys("_", legacyOrderKeys)

	addressColumns := map[string]string{
		"first_name": strategyFirstName,
		"last_name":  strategyLastName,
		"company":    strategyCompany,
		"address_1":  strategyStreet,
		"address_2":  strategyBlank,
		"city":       strategyCity,
		"postcode":   strategyPostcode,
		"email":      strategyEmail,
		"phone":      strategyPhone,
	}

	return pseudonymizeAdapter{
		DumpFirst: []string{"wc_customer_lookup", "wc_orders", "wc_order_addresses"},
		Protect: map[string][]string{
			"wc_product_meta_lookup":           {"sku", "global_unique_id"},
			"woocommerce_attribute_taxonomies": {"attribute_name", "attribute_label"},
			"woocommerce_order_items":          {"order_item_type"},
		},
		Tables: []tableRule{
			{
				Name:     "wc_orders",
				Identity: &identityRule{UserColumn: "customer_id", EmailColumn: "billing_email"},
				Collect:  []collectRule{{Map: "wc_orders", IDColumn: "id", StoreIdentity: true}},
				Columns: map[string]string{
					"billing_email":  strategyEmail,
					"ip_address":     strategyIP,
					"user_agent":     strategyUserAgent,
					"customer_note":  strategyBlank,
					"transaction_id": strategyToken,
				},
			},
			{
				Name:     "wc_order_addresses",
				Identity: &identityRule{LinkColumn: "order_id", LinkMap: "wc_orders", RowColumn: "order_id"},
				Columns:  addressColumns,
			},
			{
				Name:     "wc_orders_meta",
				Identity: &identityRule{LinkColumn: "order_id", LinkMap: "wc_orders", RowColumn: "order_id"},
				Meta:     standardMeta("order_id"),
			},
			{
				// billing_phone and shipping_phone go by their names: older WooCommerce versions
				// lack them, which a rule here would report as a schema change.
				Name:     "wc_customer_lookup",
				Identity: &identityRule{UserColumn: "user_id", EmailColumn: "email"},
				Columns: map[string]string{
					"username": strategyLogin, "first_name": strategyFirstName, "last_name": strategyLastName,
					"email": strategyEmail, "city": strategyCity, "postcode": strategyPostcode,
				},
			},
			{
				Name:     "woocommerce_downloadable_product_permissions",
				Identity: &identityRule{UserColumn: "user_id", EmailColumn: "user_email"},
				Columns:  map[string]string{"user_email": strategyEmail},
			},
			{Name: "wc_download_log", Columns: map[string]string{"user_ip_address": strategyIP}},
			{Name: "woocommerce_order_itemmeta", Meta: standardMeta("order_item_id")},
			// Whole customer records, credentials, and free-form logs. None of them are
			// needed for a site to run, and WooCommerce recreates what it needs.
			{Name: "woocommerce_sessions", Drop: true},
			{Name: "woocommerce_payment_tokens", Drop: true},
			{Name: "woocommerce_payment_tokenmeta", Drop: true},
			{Name: "woocommerce_api_keys", Drop: true},
			{Name: "woocommerce_log", Drop: true},
			{Name: "wc_rate_limits", Drop: true},
			{Name: "actionscheduler_logs", Drop: true},
			// A development copy must not deliver order events to live endpoints.
			{Name: "wc_webhooks", Columns: map[string]string{"status": "constant:disabled", "secret": strategyBlank, "delivery_url": strategyBlank}},
			{
				Name: "usermeta", Global: true,
				Meta: &metaRule{Keys: userKeys, DropPatterns: []string{`^_woocommerce_persistent_cart`, `^_woocommerce_tracks_anon_id$`, `^wc_last_active$`}},
			},
			{
				Name: "posts",
				Collect: []collectRule{{
					Map: privatePostsMap, IDColumn: "ID",
					Where: &ruleCondition{Column: "post_type", In: orderTypes},
				}},
				// A legacy order keeps the customer's note in the excerpt.
				Conditions: []ruleCondition{{Column: "post_type", In: orderTypes, Columns: map[string]string{"post_excerpt": strategyBlank}}},
			},
			{Name: "postmeta", Meta: &metaRule{
				Keys:            legacyOrderKeys,
				ProtectKeys:     []string{"_sku", "_global_unique_id", "_product_attributes", "_downloadable_files", "_product_image_gallery", "_children", "_upsell_ids", "_crosssell_ids", "_product_version"},
				ProtectPatterns: []string{`^attribute_`},
			}},
			{
				Name: "options",
				Meta: &metaRule{
					// The shop's own address is public, and templates and tax rules read it.
					ProtectKeys: []string{"woocommerce_store_address", "woocommerce_store_address_2", "woocommerce_store_city", "woocommerce_store_postcode", "woocommerce_default_country", "woocommerce_currency", "woocommerce_version", "woocommerce_db_version"},
					// Gateway settings hold live API keys, so a local checkout could charge real money.
					DropPatterns: []string{`^_wc_session_`, `^woocommerce[-_](?:stripe|ppcp|ppec|paypal|mollie|klarna|kco|braintree|square|authorize|amazon|woocommerce_payments)`, `^mollie-payments-for-woocommerce_`},
				},
			},
		},
	}
}

// eddAdapter covers Easy Digital Downloads 3, plus the meta keys of its legacy payments.
func eddAdapter() pseudonymizeAdapter {
	edd := "edd_customers"
	orders := "edd_orders"
	addressColumns := map[string]string{
		"name": strategyFullName, "address": strategyStreet, "address2": strategyBlank,
		"city": strategyCity, "postal_code": strategyPostcode,
	}
	return pseudonymizeAdapter{
		DumpFirst: []string{edd, orders, "edd_customer_addresses", "edd_order_addresses"},
		// Email templates: code compares these, and "customer" here is a role, not a person.
		Protect: map[string][]string{"edd_emails": {"email_id", "context", "sender", "recipient", "status"}},
		Tables: []tableRule{
			{
				// By address alone: the column is unique, and two customers can resolve to one
				// user when a guest record carries a registered user's address.
				Name:     edd,
				Identity: &identityRule{EmailColumn: "email"},
				Collect:  []collectRule{{Map: edd, IDColumn: "id", StoreIdentity: true}},
				Columns:  map[string]string{"email": strategyEmail, "name": strategyFullName},
			},
			{
				Name:     "edd_customer_addresses",
				Identity: &identityRule{LinkColumn: "customer_id", LinkMap: edd, RowColumn: "customer_id"},
				Columns:  addressColumns,
			},
			{
				Name:     "edd_customer_email_addresses",
				Identity: &identityRule{EmailColumn: "email"},
				Columns:  map[string]string{"email": strategyEmail},
			},
			{Name: "edd_customermeta", Meta: standardMeta("edd_customer_id")},
			{
				Name:     orders,
				Identity: &identityRule{UserColumn: "user_id", EmailColumn: "email", LinkColumn: "customer_id", LinkMap: edd},
				Collect:  []collectRule{{Map: orders, IDColumn: "id", StoreIdentity: true}},
				Columns:  map[string]string{"email": strategyEmail, "ip": strategyIP, "payment_key": strategyToken},
			},
			{
				Name:     "edd_order_addresses",
				Identity: &identityRule{LinkColumn: "order_id", LinkMap: orders, RowColumn: "order_id"},
				Columns:  addressColumns,
			},
			{Name: "edd_ordermeta", Meta: standardMeta("edd_order_id")},
			{Name: "edd_order_itemmeta", Meta: standardMeta("edd_order_item_id")},
			{Name: "edd_order_transactions", Columns: map[string]string{"transaction_id": strategyToken}},
			{Name: "edd_logs_file_downloads", Columns: map[string]string{"ip": strategyIP, "user_agent": strategyUserAgent}},
			{Name: "edd_notes", Columns: map[string]string{"content": strategyText}},
			{Name: "edd_sessions", Drop: true},
			{Name: "edd_payment_tokens", Drop: true},
			{Name: "edd_logs", Drop: true},
			{Name: "edd_logmeta", Drop: true},
			{Name: "edd_logs_emails", Drop: true},
			{Name: "edd_logs_emailmeta", Drop: true},
			{Name: "edd_logs_api_requests", Drop: true},
			{Name: "edd_logs_api_requestmeta", Drop: true},
			{
				Name:    "posts",
				Collect: []collectRule{{Map: privatePostsMap, IDColumn: "ID", Where: &ruleCondition{Column: "post_type", In: []string{"edd_payment", "edd_log"}}}},
			},
			{Name: "postmeta", Meta: &metaRule{ProtectKeys: []string{"edd_sku", "edd_download_files", "edd_variable_prices"}}},
		},
	}
}

func pseudonymizeAdapters() []pseudonymizeAdapter {
	return []pseudonymizeAdapter{wordpressAdapter(), woocommerceAdapter(), eddAdapter()}
}

// mergeTableRules folds the adapters' rules into one per table, in adapter order, so a
// plugin can extend what core says about a shared table such as postmeta.
func mergeTableRules(adapters []pseudonymizeAdapter) map[string]tableRule {
	merged := map[string]tableRule{}
	for _, adapter := range adapters {
		for _, rule := range adapter.Tables {
			merged[rule.Name] = mergeTableRule(merged[rule.Name], rule)
		}
	}
	return merged
}

func mergeTableRule(base tableRule, extra tableRule) tableRule {
	base.Name = extra.Name
	base.Global = base.Global || extra.Global
	base.Drop = base.Drop || extra.Drop
	base.RegisterUsers = base.RegisterUsers || extra.RegisterUsers
	base.Columns = mergeStrategies(base.Columns, extra.Columns)
	base.Conditions = append(base.Conditions, extra.Conditions...)
	base.Phrases = append(base.Phrases, extra.Phrases...)
	if extra.NameSkip != nil {
		base.NameSkip = extra.NameSkip
	}
	base.Collect = append(base.Collect, extra.Collect...)
	if extra.Identity != nil {
		base.Identity = extra.Identity
	}
	if extra.Meta != nil {
		if base.Meta == nil {
			base.Meta = &metaRule{}
		}
		base.Meta.merge(*extra.Meta)
	}
	return base
}

func (meta *metaRule) merge(extra metaRule) {
	meta.KeyColumn = firstNonEmpty(extra.KeyColumn, meta.KeyColumn)
	meta.ValueColumn = firstNonEmpty(extra.ValueColumn, meta.ValueColumn)
	meta.ObjectColumn = firstNonEmpty(extra.ObjectColumn, meta.ObjectColumn)
	meta.SensitivePattern = firstNonEmpty(extra.SensitivePattern, meta.SensitivePattern)
	meta.SensitiveOnlyInMap = firstNonEmpty(extra.SensitiveOnlyInMap, meta.SensitiveOnlyInMap)
	meta.Keys = mergeStrategies(meta.Keys, extra.Keys)
	meta.DropKeys = append(meta.DropKeys, extra.DropKeys...)
	meta.DropPatterns = append(meta.DropPatterns, extra.DropPatterns...)
	meta.KeepPatterns = append(meta.KeepPatterns, extra.KeepPatterns...)
	meta.ProtectKeys = append(meta.ProtectKeys, extra.ProtectKeys...)
	meta.ProtectPatterns = append(meta.ProtectPatterns, extra.ProtectPatterns...)
	meta.PhraseKeys = append(meta.PhraseKeys, extra.PhraseKeys...)
}

func mergeStrategies(base map[string]string, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

// pseudonymizeDumpOrder is the export order of the lookup tables, across all adapters.
func pseudonymizeDumpOrder(adapters []pseudonymizeAdapter) []string {
	order := []string{}
	for _, adapter := range adapters {
		order = append(order, adapter.DumpFirst...)
	}
	// People first, whichever table holds them, so the free text after them can be checked
	// for their names. Then what is mostly free text. Legacy orders live in posts, and their
	// meta needs to know which posts are orders.
	return append(order, dumpOrderPeople, "comments", "options", "sitemeta", "posts", "postmeta")
}

// dumpOrderPeople stands for the tables that look like they hold people but no adapter
// knows, which only the source can name.
const dumpOrderPeople = "@people"

// pseudonymizeSchemaOnlyTables names the tables whose rows a pull never keeps. The source
// exports only their structure, which is often most of the work on a shop, since sessions
// and logs tend to be the largest tables.
func pseudonymizeSchemaOnlyTables(tables map[string]tableRule) []string {
	names := []string{}
	for name, rule := range tables {
		if rule.Drop {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
