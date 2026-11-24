package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.LarkMomentsComment
import persona.core.contracts.LarkMomentsCommentsResponse
import persona.core.contracts.LarkMomentsPost
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LarkMomentsTest {
    /** The app's strings; Robolectric runs in Simplified Chinese (robolectric.properties). */
    private val res get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test
    fun postIdExtractedFromLarkShareLinks() {
        assertEquals(
            "7000000000000000001",
            LarkMoments.postId("https://applink.feishu.cn/client/moments/detail?postId=7000000000000000001")
        )
        assertEquals(
            "mp1",
            LarkMoments.postId("https://example.feishu.cn/moments/detail?postId=mp1&foo=bar")
        )
        assertEquals(
            "post-123_abc",
            LarkMoments.postId("https://applink.example.com/client/moments/detail?postId=post-123_abc")
        )
        assertNull(LarkMoments.postId(null))
        assertNull(LarkMoments.postId(""))
        assertNull(LarkMoments.postId("https://applink.feishu.cn/client/moments/detail"))
        assertNull(LarkMoments.postId("https://applink.feishu.cn/client/chat/open?chatId=123"))
        assertNull(LarkMoments.postId("https://example.com/other?postId=123"))
    }

    @Test
    fun aPostsHubPicturesNameItsManagerUnlessItIsTheDefault() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        try {
            assertEquals("/api/v1/lark/media/k?manager=m-3", LarkMoments.imageUrl("/api/v1/lark/media/k", "m-3"))
            assertEquals("/api/v1/lark/media/k?size=2&manager=m-3#top", LarkMoments.imageUrl("/api/v1/lark/media/k?size=2#top", "m-3"))
            assertEquals("/api/v1/lark/media/k", LarkMoments.imageUrl("/api/v1/lark/media/k", "assistant"))
            assertEquals("/api/v1/lark/media/k", LarkMoments.imageUrl("/api/v1/lark/media/k", null))
            assertEquals("/api/v1/lark/media/k?manager=m-1", LarkMoments.imageUrl("/api/v1/lark/media/k?manager=m-1", "m-3"))
            assertEquals("https://cdn.example.com/k.jpg", LarkMoments.imageUrl("https://cdn.example.com/k.jpg", "m-3"))
        } finally {
            AssistantManagerChoice.reset()
        }
    }

    @Test
    fun kindLabelDistinguishesMentionsFromGeneralMoments() {
        val atMePost = InboxRecord(
            item = InboxItem("i1", kind = "moment"),
            moment = InboxMomentRef(postId = "p1", notice = "atInPost")
        )
        assertEquals("@我", kindLabel(res, atMePost))

        val atMeComment = InboxRecord(
            item = InboxItem("i2", kind = "moment"),
            moment = InboxMomentRef(postId = "p1", notice = "atInComment")
        )
        assertEquals("@我", kindLabel(res, atMeComment))

        val comment = InboxRecord(
            item = InboxItem("i3", kind = "moment"),
            moment = InboxMomentRef(postId = "p1", notice = "comment")
        )
        assertEquals("公司圈", kindLabel(res, comment))

        val reply = InboxRecord(
            item = InboxItem("i4", kind = "moment"),
            moment = InboxMomentRef(postId = "p1", notice = "reply")
        )
        assertEquals("公司圈", kindLabel(res, reply))
    }

    @Test
    fun wireModelsDecodeMomentsPostAndComments() {
        val postJson = """
            {
                "id": "mp1",
                "userName": "林小雨",
                "userId": "u1",
                "userAvatarUrl": "/api/v1/lark/media/avatar-1",
                "commentCount": 3,
                "reactionCount": 12,
                "createTime": 1730689200000.0,
                "content": {
                    "text": "换新电脑数据迁移",
                    "segments": [
                        {"kind": "text", "text": "换新电脑数据迁移 "},
                        {"kind": "emoji", "key": "SMILE", "text": "[微笑]"}
                    ]
                },
                "images": [
                    {"url": "/api/v1/lark/media/img-1", "width": 600.0, "height": 600.0}
                ],
                "reactionGroups": [
                    {"type": "THUMBSUP", "count": 12, "selfInvolved": true}
                ]
            }
        """.trimIndent()

        val post = JsonCodec.decodeFromString<LarkMomentsPost>(postJson)
        assertEquals("mp1", post.id)
        assertEquals("林小雨", post.userName)
        assertEquals(3, post.commentCount)
        assertEquals(12, post.reactionCount)
        assertEquals("换新电脑数据迁移", post.content?.text)
        assertEquals(2, post.content?.segments?.size)
        assertEquals(1, post.images?.size)
        assertEquals(true, post.reactionGroups?.first()?.selfInvolved)

        val commentsJson = """
            {
                "comments": [
                    {
                        "id": "mc1",
                        "userName": "陈子墨",
                        "userId": "u2",
                        "createTime": 1730691600000.0,
                        "content": {"text": "插雷电线快很多"}
                    },
                    {
                        "id": "mc2",
                        "userName": "周一航",
                        "userId": "u3",
                        "createTime": 1730694000000.0,
                        "replyUserName": "陈子墨",
                        "content": {"text": "同感"}
                    }
                ]
            }
        """.trimIndent()

        val comments = JsonCodec.decodeFromString<LarkMomentsCommentsResponse>(commentsJson).comments
        assertEquals(2, comments.size)
        assertEquals("mc1", comments[0].id)
        assertEquals("陈子墨", comments[0].userName)
        assertEquals("mc2", comments[1].id)
        assertEquals("陈子墨", comments[1].replyUserName)
    }

    @Test
    fun inboxItemWireModelDecodesMomentField() {
        val wireJson = """
            {
                "id": "moment:mp1:mn1",
                "source": "lark",
                "kind": "moment",
                "account": "feishu",
                "account_name": "飞书",
                "title": "换新电脑数据迁移",
                "sender": "林小雨",
                "preview": "迁移助手跑了一下午",
                "count": 3,
                "tier": "action",
                "moment": {
                    "post_id": "mp1",
                    "notice": "comment",
                    "reply_to": "mc1",
                    "my_post": true,
                    "post_text": "换新电脑数据迁移"
                }
            }
        """.trimIndent()

        val item = JsonCodec.decodeFromString<persona.core.contracts.InboxItem>(wireJson)
        assertEquals("moment:mp1:mn1", item.id)
        assertEquals("moment", item.kind)
        assertEquals("mp1", item.moment?.postId)
        assertEquals("comment", item.moment?.notice)
        assertEquals("mc1", item.moment?.replyTo)
        assertEquals(true, item.moment?.myPost)
        assertEquals("换新电脑数据迁移", item.moment?.postText)
    }
}
