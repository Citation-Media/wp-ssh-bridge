<?php
/**
 * Plugin Name: wp-ssh-bridge E2E page builder stand-ins
 * Description: Registers the WP-CLI commands the CLI runs to rebuild page builder CSS, without installing the builders. Each run leaves a timestamp option to assert on.
 */

if ( ! defined( 'WP_CLI' ) || ! WP_CLI ) {
	return;
}

// `wp elementor flush-css [--network]` always succeeds.
WP_CLI::add_command(
	'elementor flush-css',
	static function ( $args, $assoc_args ) {
		update_option( 'wpsb_e2e_elementor_flushed', gmdate( 'c' ), false );
		WP_CLI::success( 'Elementor CSS flushed (wp-ssh-bridge E2E stand-in).' );
	}
);

// `wp bricks regenerate_assets` exists only when a scenario asks for a
// failing builder, so the CLI's warning path can be observed. Otherwise the
// command stays unregistered, which the CLI skips silently.
if ( get_option( 'wpsb_e2e_fail_bricks' ) ) {
	WP_CLI::add_command(
		'bricks regenerate_assets',
		static function () {
			WP_CLI::error( 'Bricks asset regeneration failed (wp-ssh-bridge E2E stand-in).' );
		}
	);
}
