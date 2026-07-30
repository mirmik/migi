package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Test

class PickerResultPolicyTest {
    @Test
    fun cancelledPickerDoesNotBecomeAnUpload() {
        assertEquals(
            PickerResultPolicy.Outcome.CANCELLED,
            PickerResultPolicy.classify(succeeded = false, hasUri = false),
        )
        assertEquals(
            PickerResultPolicy.Outcome.CANCELLED,
            PickerResultPolicy.classify(succeeded = false, hasUri = true),
        )
    }

    @Test
    fun successfulPickerRequiresAUri() {
        assertEquals(
            PickerResultPolicy.Outcome.MISSING_URI,
            PickerResultPolicy.classify(succeeded = true, hasUri = false),
        )
        assertEquals(
            PickerResultPolicy.Outcome.SELECTED,
            PickerResultPolicy.classify(succeeded = true, hasUri = true),
        )
    }
}
