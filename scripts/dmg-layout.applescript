-- Lay out the NetStack Doctor DMG window so the install gesture is obvious:
-- the app on the left, an alias of /Applications on the right, drag one onto
-- the other. The Gatekeeper installer and the read-me sit on a second row.
--
-- Finder records this into a .DS_Store on the mounted read/write image, which
-- make-dmg.sh then converts to the compressed image that ships.
--
-- Usage: osascript scripts/dmg-layout.applescript "<mounted volume name>"

on run argv
	set volName to item 1 of argv
	tell application "Finder"
		tell disk volName
			open
			set current view of container window to icon view
			set toolbar visible of container window to false
			set statusbar visible of container window to false
			set the bounds of container window to {200, 140, 900, 620}

			set viewOpts to the icon view options of container window
			set arrangement of viewOpts to not arranged
			set icon size of viewOpts to 128
			set text size of viewOpts to 13

			set position of item "NetStack Doctor.app" of container window to {170, 180}
			set position of item "Applications" of container window to {530, 180}

			-- Both are optional extras; don't fail the layout if either is absent.
			try
				set position of item "Install — Bypass Gatekeeper.command" of container window to {170, 370}
			end try
			try
				set position of item "READ ME FIRST.txt" of container window to {530, 370}
			end try

			update without registering applications
			delay 2
			close
		end tell
	end tell
end run
