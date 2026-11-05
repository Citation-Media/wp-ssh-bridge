<?php
/**
 * Plants personal data in the tables only a multisite network has. Runs through
 * `wp eval-file` from `wpsb-seed source-multisite`.
 */

global $wpdb;

$wpdb->insert(
	$wpdb->base_prefix . 'signups',
	array(
		'domain'         => 'source.wpsb.test',
		'path'           => '/',
		'title'          => '',
		'user_login'     => 'canarysignup',
		'user_email'     => 'canary-signup@realmail.test',
		'registered'     => current_time( 'mysql' ),
		'activated'      => '0000-00-00 00:00:00',
		'activation_key' => 'canary-activation',
		'meta'           => maybe_serialize( array( 'note' => 'canary signup' ) ),
	)
);
$wpdb->insert(
	$wpdb->base_prefix . 'registration_log',
	array(
		'email'           => 'canary-registered@realmail.test',
		'IP'              => '203.0.113.20',
		'blog_id'         => 2,
		'date_registered' => current_time( 'mysql' ),
	)
);
update_site_option( 'admin_email', 'canary-network-admin@realmail.test' );

$author = get_user_by( 'login', 'canarynetwork' );
switch_to_blog( 2 );
update_option( 'admin_email', 'canary-second-admin@realmail.test' );
$post = wp_insert_post(
	array(
		'post_title'  => 'Subsite post',
		'post_status' => 'publish',
	)
);
wp_insert_comment(
	array(
		'comment_post_ID'      => $post,
		'comment_author'       => 'Canary Network',
		'comment_author_email' => 'canary-network@realmail.test',
		'comment_author_IP'    => '203.0.113.21',
		'comment_content'      => 'Canary subsite comment',
		'user_id'              => $author->ID,
	)
);
restore_current_blog();
