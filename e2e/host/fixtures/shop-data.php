<?php
/**
 * Fills the real WooCommerce and Easy Digital Downloads with customers. Runs through
 * `wp eval-file` from `wpsb-seed source-shop`, with the order store as its argument.
 *
 * Every personal value carries "canary", or sits in 203.0.113.0/24 or realmail.test, so a
 * scenario can search whole databases for leaks. The plugin APIs fill the tables where
 * they can; tables without a stable API are written directly, which still fails loudly
 * when a column name no longer exists.
 */

global $wpdb;

$wpsb_mode = $args[0] ?? 'both';

// EDD otherwise creates its tables on the first admin request, which WP-CLI never makes.
edd_install_component_database_tables();

/**
 * @param string $table Relative to the table prefix.
 * @param array  $row   Column values.
 */
function wpsb_insert( string $table, array $row ): int {
	global $wpdb;
	$wpdb->show_errors( false );
	if ( false === $wpdb->insert( $wpdb->prefix . $table, $row ) ) {
		WP_CLI::error( "Could not fill {$table}: {$wpdb->last_error}" );
	}
	return (int) $wpdb->insert_id;
}

function wpsb_person( string $who ): array {
	return array(
		'first_name' => "Canary{$who}",
		'last_name'  => 'Lastcanary',
		'company'    => 'Canary GmbH',
		'address_1'  => 'Canarystrasse 1',
		'address_2'  => 'Canary Floor',
		'city'       => 'Canarytown',
		'state'      => 'BE',
		'postcode'   => 'CANARY',
		'country'    => 'DE',
		'phone'      => '+49 canary 555',
	);
}

$hpos = wc_get_container()->get( \Automattic\WooCommerce\Internal\DataStores\Orders\DataSynchronizer::class );
if ( 'legacy' === $wpsb_mode ) {
	update_option( 'woocommerce_custom_orders_table_enabled', 'no' );
	update_option( 'woocommerce_custom_orders_table_data_sync_enabled', 'no' );
} else {
	$hpos->create_database_tables();
	update_option( 'woocommerce_custom_orders_table_enabled', 'yes' );
	update_option( 'woocommerce_custom_orders_table_data_sync_enabled', 'yes' );
}

// --- Accounts and comments ------------------------------------------------------------

update_option( 'admin_email', 'canary-owner@realmail.test' );
update_option( 'woocommerce_email_from_address', 'canary-shop@realmail.test' );
update_option( 'wpsb_e2e_serialized', array( 'contact' => 'canary-contact@realmail.test', 'label' => 'kept' ) );
update_option( 'woocommerce_stripe_settings', array( 'secret_key' => 'sk_live_canary' ) );
update_option( 'mailserver_pass', 'canary-mail-password' );
edd_update_option( 'paypal_live_client_secret', 'canary-paypal-secret' );
set_transient( 'wpsb_e2e_cached', 'canary cached customer', 3600 );

$anna = wp_insert_user(
	array(
		'role'         => 'customer',
		'user_login'   => 'canary-anna',
		'user_pass'    => 'canary-password',
		'user_email'   => 'canary-anna@realmail.test',
		'display_name' => 'Canary Anna',
		'first_name'   => 'CanaryAnna',
		'last_name'    => 'Lastcanary',
		'nickname'     => 'canary-nick',
		'description'  => 'Canary biography',
		'user_url'     => 'https://canary.realmail.test',
	)
);
if ( 2 !== $anna ) {
	WP_CLI::error( 'The scenarios expect the customer to be user 2, not ' . wp_json_encode( $anna ) );
}

$customer = new WC_Customer( $anna );
foreach ( wpsb_person( 'Anna' ) as $field => $value ) {
	$customer->{"set_billing_{$field}"}( $value );
	if ( 'phone' !== $field ) {
		$customer->{"set_shipping_{$field}"}( $value );
	}
}
$customer->set_billing_email( 'canary-anna@realmail.test' );
$customer->save();
update_user_meta( $anna, 'community-events-location', array( 'ip' => '203.0.113.7' ) );
update_user_meta( $anna, '_woocommerce_persistent_cart_1', array( 'note' => 'canary cart' ) );
update_user_meta( $anna, 'session_tokens', array( 'canary-token' => array( 'ip' => '203.0.113.7' ) ) );
update_user_meta( $anna, '_application_passwords', array( array( 'name' => 'canary-app' ) ) );

$post = wp_insert_post(
	array(
		'post_title'  => 'Commented post',
		'post_status' => 'publish',
	)
);
wp_insert_comment(
	array(
		'comment_post_ID'      => $post,
		'comment_author'       => 'Canary Anna',
		'comment_author_email' => 'canary-anna@realmail.test',
		'comment_author_url'   => 'https://canary.realmail.test',
		'comment_author_IP'    => '203.0.113.7',
		'comment_agent'        => 'CanaryBrowser/1.0',
		'comment_content'      => 'Canary comment text',
		'comment_approved'     => 1,
		'user_id'              => $anna,
	)
);
$guest_comment = wp_insert_comment(
	array(
		'comment_post_ID'      => $post,
		'comment_author'       => 'Canary Guest',
		'comment_author_email' => 'canary-guest@realmail.test',
		'comment_author_IP'    => '203.0.113.8',
		'comment_content'      => 'Canary guest text',
		'comment_approved'     => 1,
	)
);
add_comment_meta( $guest_comment, 'akismet_as_submitted', array( 'comment_author' => 'Canary Guest' ) );

wp_create_user_request( 'canary-privacy@realmail.test', 'export_personal_data' );

// --- WooCommerce ----------------------------------------------------------------------

$product = new WC_Product_Simple();
$product->set_name( 'WPSB E2E Product' );
$product->set_regular_price( '19.99' );
$product->set_virtual( true );
$product->set_downloadable( true );
$download = new WC_Product_Download();
$download->set_id( 'wpsb-e2e-download' );
$download->set_name( 'File' );
// WooCommerce only accepts a file that exists in an approved directory.
$uploads = wp_upload_dir();
wp_mkdir_p( $uploads['basedir'] . '/woocommerce_uploads' );
file_put_contents( $uploads['basedir'] . '/woocommerce_uploads/wpsb-e2e.zip', 'protected download' );
$download->set_file( $uploads['baseurl'] . '/woocommerce_uploads/wpsb-e2e.zip' );
$product->set_downloads( array( $download ) );
$product->save();

/**
 * @param int    $customer_id Zero for a guest.
 * @param string $email       Billing address.
 * @param string $who         Tells the planted names apart.
 */
function wpsb_order( WC_Product $product, int $customer_id, string $email, string $who ): WC_Order {
	$order = wc_create_order( array( 'customer_id' => $customer_id, 'status' => 'pending' ) );
	$order->add_product( $product, 1 );
	foreach ( wpsb_person( $who ) as $field => $value ) {
		$order->{"set_billing_{$field}"}( $value );
		$order->{"set_shipping_{$field}"}( $value );
	}
	$order->set_billing_email( $email );
	$order->set_customer_ip_address( '203.0.113.9' );
	$order->set_customer_user_agent( 'CanaryBrowser/1.0' );
	$order->set_customer_note( 'Canary delivery note' );
	$order->set_transaction_id( "canary-tx-{$who}" );
	$order->set_payment_method( 'bacs' );
	$order->update_meta_data( '_billing_vat_id', 'CANARY-VAT' );
	$order->update_meta_data( 'Payer PayPal address', 'canary-paypal@realmail.test' );
	$order->calculate_totals();
	$order->set_status( 'completed' );
	$order->save();
	$order->add_order_note( 'Canary called about the order' );
	return $order;
}

$registered = wpsb_order( $product, $anna, 'canary-anna@realmail.test', 'Anna' );
// A guest checkout under a registered address, in other case: the same person.
wpsb_order( $product, 0, 'CANARY-ANNA@realmail.test', 'AnnaGuest' );
wpsb_order( $product, 0, 'canary-guest@realmail.test', 'Guest' );
wpsb_order( $product, 0, 'canary-guest@realmail.test', 'GuestAgain' );

wc_downloadable_file_permission( 'wpsb-e2e-download', $product->get_id(), $registered );
$permission = (int) $wpdb->get_var( "SELECT permission_id FROM {$wpdb->prefix}woocommerce_downloadable_product_permissions LIMIT 1" );
wpsb_insert(
	'wc_download_log',
	array(
		'timestamp'       => current_time( 'mysql' ),
		'permission_id'   => $permission,
		'user_id'         => $anna,
		'user_ip_address' => '203.0.113.10',
	)
);
\Automattic\WooCommerce\Admin\API\Reports\Customers\DataStore::update_registered_customer( $anna );
wpsb_insert(
	'wc_customer_lookup',
	array(
		'user_id'    => null,
		'username'   => '',
		'first_name' => 'CanaryGuest',
		'last_name'  => 'Lastcanary',
		'email'      => 'canary-guest@realmail.test',
		'country'    => 'DE',
		'postcode'   => 'CANARY',
		'city'       => 'Canarytown',
		'state'      => 'BE',
	)
);
wpsb_insert(
	'woocommerce_sessions',
	array(
		'session_key'    => 'canarykey',
		'session_value'  => maybe_serialize( array( 'customer' => 'canary-session@realmail.test' ) ),
		'session_expiry' => time() + 3600,
	)
);
$token = wpsb_insert(
	'woocommerce_payment_tokens',
	array(
		'gateway_id' => 'stripe',
		'token'      => 'canary-card-token',
		'user_id'    => $anna,
		'type'       => 'CC',
	)
);
wpsb_insert(
	'woocommerce_payment_tokenmeta',
	array(
		'payment_token_id' => $token,
		'meta_key'         => 'last4',
		'meta_value'       => 'canary',
	)
);
wpsb_insert(
	'woocommerce_log',
	array(
		'timestamp' => current_time( 'mysql' ),
		'level'     => 100,
		'source'    => 'wpsb',
		'message'   => 'Canary customer logged',
	)
);
wpsb_insert(
	'woocommerce_api_keys',
	array(
		'user_id'         => $anna,
		'description'     => 'Canary key',
		'permissions'     => 'read',
		'consumer_key'    => 'canary',
		'consumer_secret' => 'canary',
		'truncated_key'   => 'canary',
	)
);
wpsb_insert(
	'wc_webhooks',
	array(
		'status'            => 'active',
		'name'              => 'Live endpoint',
		'user_id'           => 1,
		'delivery_url'      => 'https://hooks.example.test/live',
		'secret'            => 'canary-webhook-secret',
		'topic'             => 'order.created',
		'date_created'      => current_time( 'mysql' ),
		'date_created_gmt'  => current_time( 'mysql', true ),
		'api_version'       => 3,
		'failure_count'     => 0,
		'pending_delivery'  => 0,
	)
);

// --- Easy Digital Downloads -----------------------------------------------------------

$edd_customer = edd_add_customer(
	array(
		'user_id' => $anna,
		'email'   => 'canary-anna@realmail.test',
		'name'    => 'Canary Anna',
	)
);
edd_add_customer_address(
	array(
		'customer_id' => $edd_customer,
		'name'        => 'Canary Anna',
		'address'     => 'Canarystrasse 1',
		'address2'    => 'Canary Floor',
		'city'        => 'Canarytown',
		'postal_code' => 'CANARY',
		'country'     => 'DE',
	)
);
edd_add_customer_email_address(
	array(
		'customer_id' => $edd_customer,
		'email'       => 'canary-second@realmail.test',
	)
);
edd_add_customer_meta( $edd_customer, 'phone', 'canary 555' );
$edd_order = edd_add_order(
	array(
		'user_id'     => $anna,
		'customer_id' => $edd_customer,
		'email'       => 'canary-anna@realmail.test',
		'ip'          => '203.0.113.11',
		'payment_key' => 'canary-payment-key',
		'status'      => 'complete',
		'total'       => 5,
	)
);
edd_add_order_address(
	array(
		'order_id'    => $edd_order,
		'name'        => 'Canary Anna',
		'address'     => 'Canarystrasse 1',
		'address2'    => 'Canary Floor',
		'city'        => 'Canarytown',
		'postal_code' => 'CANARY',
		'country'     => 'DE',
	)
);
edd_add_order_meta( $edd_order, 'user_info', array( 'first_name' => 'CanaryAnna' ) );
edd_add_order_transaction(
	array(
		'object_id'      => $edd_order,
		'object_type'    => 'order',
		'transaction_id' => 'canary-edd-tx',
		'gateway'        => 'manual',
		'status'         => 'complete',
		'total'          => 5,
	)
);
edd_add_note(
	array(
		'object_id'   => $edd_order,
		'object_type' => 'order',
		'content'     => 'Canary called',
	)
);
wpsb_insert(
	'edd_logs_file_downloads',
	array(
		'product_id'  => 1,
		'order_id'    => $edd_order,
		'customer_id' => $edd_customer,
		'ip'          => '203.0.113.12',
		'user_agent'  => 'CanaryBrowser/1.0',
	)
);

// --- A plugin table nobody wrote a rule for -------------------------------------------

$wpdb->query( "CREATE TABLE {$wpdb->prefix}wpsb_crm_contacts (id INT PRIMARY KEY, email VARCHAR(100), full_name VARCHAR(100), phone VARCHAR(30), street VARCHAR(100), date_of_birth DATE, phone_label VARCHAR(30))" );
wpsb_insert( 'wpsb_crm_contacts', array( 'id' => 1, 'email' => 'canary-anna@realmail.test', 'full_name' => 'Canary Crm', 'phone' => '+49 170 5550125', 'street' => 'Canary Street 5', 'date_of_birth' => '1980-02-03', 'phone_label' => 'Telefon' ) );
wpsb_insert( 'wpsb_crm_contacts', array( 'id' => 2, 'email' => 'canary-crm@realmail.test', 'full_name' => 'Canary Crm', 'phone' => '0170 5550126', 'street' => 'Canary Street 6', 'date_of_birth' => '1980-02-03', 'phone_label' => 'Telefon' ) );

// Free text, which only the scan can read. Every value in it counts as planted.
$wpdb->query( "CREATE TABLE {$wpdb->prefix}wpsb_notes (id INT PRIMARY KEY, body TEXT, created DATETIME)" );
wpsb_insert(
	'wpsb_notes',
	array(
		'id'      => 1,
		'body'    => 'Call Canary Anna at +49 170 5550123 or 0170 5550124, from 203.0.113.9 or 2001:4860:4860::8888. IBAN DE89 3704 0044 0532 0130 00, card 4111 1111 1111 1111, address canary-note@realmail.test. WooCommerce 8.6.1.0 stays.',
		'created' => '2026-01-01 10:00:00',
	)
);

// A plugin's own people, in a table named so that the export reaches it after the log that
// mentions them, unless the filter asks for people first. All lower case and with an odd
// gap, as text often has it, and signed in the same row.
$wpdb->query( "CREATE TABLE {$wpdb->prefix}wpsb_aaa_log (id INT PRIMARY KEY, body TEXT)" );
wpsb_insert( 'wpsb_aaa_log', array( 'id' => 1, 'body' => 'canaryticket   lastcanary and CANARYCOMMENTER LASTCANARY called' ) );
$wpdb->query( "CREATE TABLE {$wpdb->prefix}wpsb_tickets (id INT PRIMARY KEY, first_name VARCHAR(50), last_name VARCHAR(50), email VARCHAR(100), message TEXT)" );
wpsb_insert( 'wpsb_tickets', array( 'id' => 1, 'first_name' => 'Canaryticket', 'last_name' => 'Lastcanary', 'email' => 'canary-ticket@realmail.test', 'message' => 'Thanks, canaryticket lastcanary' ) );
wp_insert_comment(
	array(
		'comment_post_ID'      => $post,
		'comment_author'       => 'Canarycommenter Lastcanary',
		'comment_author_email' => 'canary-commenter@realmail.test',
		'comment_content'      => 'Canary second comment',
		'comment_approved'     => 1,
	)
);

// What the site needs as it is: an article number shaped like a phone number, a slug too,
// and a brand that shares its name with a commenter.
$wpsb_product = wp_insert_post(
	array(
		'post_title'   => 'Protected product',
		'post_name'    => '0170-5551234',
		'post_type'    => 'product',
		'post_status'  => 'publish',
		'post_content' => 'Worn by Protectedbrand Label since 1970.',
	)
);
update_post_meta( $wpsb_product, '_sku', '0123-456789' );
wp_insert_term( 'Protectedbrand Label', 'category' );
wp_insert_comment(
	array(
		'comment_post_ID'      => $post,
		'comment_author'       => 'Protectedbrand Label',
		'comment_author_email' => 'canary-brand@realmail.test',
		'comment_content'      => 'Canary third comment',
		'comment_approved'     => 1,
	)
);

// How form plugins keep their entries: one row per field.
$wpdb->query( "CREATE TABLE {$wpdb->prefix}wpsb_form_entries (id INT PRIMARY KEY, entry_id INT, meta_key VARCHAR(100), meta_value TEXT)" );
wpsb_insert( 'wpsb_form_entries', array( 'id' => 1, 'entry_id' => 1, 'meta_key' => 'phone', 'meta_value' => '0170 5550127' ) );
wpsb_insert( 'wpsb_form_entries', array( 'id' => 2, 'entry_id' => 1, 'meta_key' => 'first_name', 'meta_value' => 'Canaryform' ) );
wpsb_insert( 'wpsb_form_entries', array( 'id' => 3, 'entry_id' => 1, 'meta_key' => 'colour', 'meta_value' => 'blue' ) );
