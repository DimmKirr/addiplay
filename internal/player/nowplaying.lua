-- addiplay nowplaying: receive structured track metadata from the Go app
-- via script-message and set force-media-title for macOS Now Playing / MPRIS.

mp.register_script_message("set-track-metadata", function(artist, title)
    local display = ""
    if artist and artist ~= "" and title and title ~= "" then
        display = artist .. " \xe2\x80\x94 " .. title
    elseif title and title ~= "" then
        display = title
    elseif artist and artist ~= "" then
        display = artist
    end
    if display ~= "" then
        mp.set_property("force-media-title", display)
    end
end)

mp.register_script_message("clear-track-metadata", function()
    mp.del_property("force-media-title")
end)
