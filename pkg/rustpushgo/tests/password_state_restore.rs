use rustpush::passwords::{PasswordState, SavedPasswordGroup, ShareInviteContentData};

fn populated_state() -> plist::Dictionary {
    let mut state = PasswordState::default();
    state.groups.insert("synthetic-group".into(), SavedPasswordGroup::default());
    state.invite_groups.insert("synthetic-invite".into(), ShareInviteContentData {
        invitation_token: vec![1, 2, 3],
        group_id: "synthetic-group".into(),
        sent_time: std::time::UNIX_EPOCH.into(),
        group_name: "Synthetic group".into(),
        share_url: "https://example.invalid/share".into(),
        invitee_handle: "synthetic@example.invalid".into(),
    });
    let mut bytes = Vec::new();
    plist::to_writer_binary(&mut bytes, &state).unwrap();
    let mut fixture: plist::Dictionary = plist::from_bytes(&bytes).unwrap();
    // Populate continuation tokens as a legacy synced state would contain.
    fixture.insert("zone_continuation_token".into(), plist::Value::Data(vec![1]));
    fixture.insert("shared_zone_continuation_token".into(), plist::Value::Data(vec![2]));
    fixture.insert("my_token_registered".into(), plist::Value::Data(vec![7; 32]));
    fixture.get_mut("groups").unwrap().as_dictionary_mut().unwrap()
        .get_mut("synthetic-group").unwrap().as_dictionary_mut().unwrap()
        .insert("sync_continuation_token".into(), plist::Value::Data(vec![3]));
    fixture
}

fn assert_preserved(state: &PasswordState) {
    assert!(state.groups.contains_key("synthetic-group"));
    let invite = state.invite_groups.get("synthetic-invite").expect("pending invite preserved");
    assert_eq!(invite.invitation_token, vec![1, 2, 3]);
    assert_eq!(invite.group_id, "synthetic-group");
    assert_eq!(invite.group_name, "Synthetic group");
}

#[test]
fn legacy_password_state_preserves_groups_and_pending_invites() {
    for old_registration in [None, Some(false), Some(true)] {
        let mut bytes = Vec::new();
        let mut legacy = populated_state();
        legacy.remove("my_token_registered");
        if let Some(registered) = old_registration {
            legacy.insert("token_registered".into(), plist::Value::Boolean(registered));
        }
        bytes.clear();
        plist::to_writer_binary(&mut bytes, &legacy).unwrap();

        let restored: PasswordState = plist::from_bytes(&bytes).expect("legacy state loads");
        assert_preserved(&restored);
        // The old boolean cannot identify the current APNs token: re-register.
        assert_eq!(restored.my_token_registered, None);

        // Persisting the migrated state must preserve its durable contents too.
        bytes.clear();
        plist::to_writer_binary(&mut bytes, &restored).unwrap();
        let round_trip: PasswordState = plist::from_bytes(&bytes).unwrap();
        assert_preserved(&round_trip);
        assert_eq!(round_trip.my_token_registered, None);
    }
}

#[test]
fn current_password_state_preserves_registered_token() {
    let state = populated_state();
    let mut bytes = Vec::new();
    plist::to_writer_binary(&mut bytes, &state).unwrap();
    let restored: PasswordState = plist::from_bytes(&bytes).unwrap();
    assert_preserved(&restored);
    assert_eq!(restored.my_token_registered, Some([7; 32]));
}
